import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { unsavedChanges } from '$lib/stores/guard';
import { startLive } from '$lib/stores/live';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch } from '../../vitest-setup';

// Keep the plain textarea and fallback diff: the CodeMirror chunk is exercised in the YamlEditor and DiffView tests.
vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));

import { App } from '../App';
import { appService, installHarness, mountDetail, operation, removeHarness, stackDetail, type Harness } from './detailTestKit';

let h: Harness;
let routes: Parameters<typeof installFakeFetch>[0];
let fake: FakeFetch;
beforeEach(() => {
  h = installHarness();
  h.operations = [];
  h.stream.restore(); // unwind in reverse order of installation
  h.fake.restore();
  routes = {
    'GET /api/stacks/web': () => h.stackRes,
    'GET /api/stacks/web/operations': () => ({ body: h.operations }),
    'GET /api/stacks/web/health?span=60m': { body: { from: 0, to: 3600, services: [] } },
    'GET /api/stacks/web/metrics': { body: { points: [] } },
    'GET /api/stacks/web/compose': { body: { content: 'services: {}\n', hash: 'h1' } },
    'POST /api/stacks/web/services/app/preview': { body: { content: 'services: {}\n', hash: 'h1' } },
    'PUT /api/stacks/web/services/app': { body: { hash: 'h2' } },
    'POST /api/stacks/web/actions/up': { body: operation() },
    'GET /api/operations/op1': { body: { ...operation(), lines: [] } },
    'GET /api/networks': { body: [] },
    'GET /api/status': { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
    'GET /api/stacks': { body: [] },
    'GET /api/operations?state=running': { body: [] },
    'GET /api/me': { body: { username: 'admin' } },
    'GET /api/setup/status': { body: { needsSetup: false } },
    'GET /api/host': { body: {} },
  };
  fake = installFakeFetch(routes);
  h.fake = fake;
  // The wider fake fetch replaced the one the stream fake wraps, so the stream fake is installed again on top.
  h.stream = installFakeStream({
    '/api/events': [],
    '/api/stacks/web/logs': [],
    '/api/stacks/web/metrics/live': [],
    '/api/operations/op1/stream': [],
  });
  toasts.reset();
});
afterEach(async () => {
  cleanup();
  await removeHarness(h);
});

const user = () => userEvent.setup();
const here = () => screen.getByTestId('location').textContent;
const rail = () => within(screen.getByRole('navigation', { name: 'Services' }));
const dialog = () => screen.findByRole('alertdialog', { name: 'Discard unsaved changes?' });
const stackGets = () => fake.calls.filter((c) => c.method === 'GET' && c.path === '/api/stacks/web').length;
const withCache = (hash = 'h1', extra: string[] = []) =>
  stackDetail({
    hash,
    services: [appService(), appService({ name: 'cache', config: { image: 'redis:7', limits: {} }, containers: [] }), ...extra.map((name) => appService({ name, containers: [] }))],
  });

async function editImage(value: string) {
  const image = (await screen.findByLabelText('Image')) as HTMLInputElement;
  await user().clear(image);
  await user().type(image, value);
}

describe('service editor on the stack page', () => {
  it('Edit opens the editor in place of the tabs and Close editor restores them', async () => {
    mountDetail(h, '/stacks/web?service=app');
    await user().click(await screen.findByRole('button', { name: 'Edit app' }));
    await screen.findByLabelText('Image');
    expect(screen.queryByRole('tablist')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Edit app' })).toBeNull();
    await user().click(screen.getByRole('button', { name: 'Close editor' }));
    expect(await screen.findByRole('tablist', { name: 'Service sections' })).toBeTruthy();
    expect(screen.queryByLabelText('Image')).toBeNull();
  });

  it('moves keyboard focus into the editor when Edit is clicked', async () => {
    mountDetail(h, '/stacks/web?service=app');
    await user().click(await screen.findByRole('button', { name: 'Edit app' }));
    await screen.findByLabelText('Image');
    await waitFor(() => expect(['INPUT', 'SELECT', 'TEXTAREA']).toContain(document.activeElement?.tagName));
  });

  it('the service editor is mounted from Edit and keeps its state when the stack refetches', async () => {
    mountDetail(h, '/stacks/web?service=app');
    await user().click(await screen.findByRole('button', { name: 'Edit app' }));
    await editImage('nginx:2');
    h.stackRes = { body: stackDetail({ hash: 'h2' }) };
    startLive({ fetchStatus: () => fetch('/api/status').then((r) => r.json()) });
    const before = stackGets();
    await waitFor(() => expect(stackGets()).toBeGreaterThan(before));
    expect((screen.getByLabelText('Image') as HTMLInputElement).value).toBe('nginx:2');
  });

  it('the service editor Apply is not soft-disabled by a running operation of the page', async () => {
    h.operations = [operation()];
    mountDetail(h, '/stacks/web?service=app');
    await user().click(await screen.findByRole('button', { name: 'Edit app' }));
    await editImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await screen.findByTestId('diff-view');
    await user().click(screen.getByRole('button', { name: 'Save' }));
    const apply = await screen.findByRole('button', { name: 'Apply' });
    expect(apply.getAttribute('aria-disabled')).toBeNull();
    expect(screen.getByRole('button', { name: 'Up' }).getAttribute('aria-disabled')).toBe('true');
  });

  it('the Logs button opens the console and keeps the open editor and its draft', async () => {
    mountDetail(h, '/stacks/web?service=app');
    await user().click(await screen.findByRole('button', { name: 'Edit app' }));
    await user().type(await screen.findByLabelText('Image'), 'x');
    await user().click(screen.getByRole('button', { name: 'Logs for app' }));
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect((screen.getByLabelText('Image') as HTMLInputElement).value).toBe('nginx:1x');
    expect(get(unsavedChanges)).toBe(true);
  });

  it('a dirty editor asks before Close editor; Cancel keeps the draft and Discard restores the tabs', async () => {
    mountDetail(h, '/stacks/web?service=app');
    await user().click(await screen.findByRole('button', { name: 'Edit app' }));
    await user().type(await screen.findByLabelText('Image'), 'x');
    await user().click(screen.getByRole('button', { name: 'Close editor' }));
    await user().click(within(await dialog()).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    expect((screen.getByLabelText('Image') as HTMLInputElement).value).toBe('nginx:1x');
    await user().click(screen.getByRole('button', { name: 'Close editor' }));
    await user().click(within(await dialog()).getByRole('button', { name: 'Discard changes' }));
    expect(await screen.findByRole('tablist', { name: 'Service sections' })).toBeTruthy();
    expect(get(unsavedChanges)).toBe(false);
  });

  it('a dirty editor asks before selecting another service, a chip, the timeline and a link; Discard changes closes it', async () => {
    window.history.replaceState(null, '', '/stacks/web?service=app');
    render(<App />);
    await user().click(await screen.findByRole('button', { name: 'Edit app' }));
    await user().type(await screen.findByLabelText('Image'), 'x');
    await waitFor(() => expect(get(unsavedChanges)).toBe(true));
    const targets = [
      () => rail().getByRole('button', { name: /^db\b/ }),
      () => within(screen.getByRole('navigation', { name: 'Service chips' })).getByRole('button', { name: /^db\b/ }),
      () => within(screen.getByRole('region', { name: 'Health · last 60 min' })).getByRole('button', { name: /^db/ }),
      () => within(screen.getByRole('navigation', { name: 'Main' })).getByRole('link', { name: 'Containers' }),
    ];
    for (const target of targets) {
      await user().click(target());
      await user().click(within(await dialog()).getByRole('button', { name: 'Cancel' }));
      await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    }
    expect(window.location.pathname + window.location.search).toBe('/stacks/web?service=app');
    expect((screen.getByLabelText('Image') as HTMLInputElement).value).toBe('nginx:1x');

    await user().click(rail().getByRole('button', { name: /^db\b/ }));
    await user().click(within(await dialog()).getByRole('button', { name: 'Discard changes' }));
    expect(await screen.findByRole('button', { name: 'Edit db' })).toBeTruthy();
    expect(screen.getByRole('tablist', { name: 'Service sections' })).toBeTruthy();
    expect(get(unsavedChanges)).toBe(false);
  });

  it('a clean editor closes on another selection without asking', async () => {
    mountDetail(h, '/stacks/web?service=app');
    await user().click(await screen.findByRole('button', { name: 'Edit app' }));
    await screen.findByLabelText('Image');
    await user().click(rail().getByRole('button', { name: /^db\b/ }));
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(await screen.findByRole('button', { name: 'Edit db' })).toBeTruthy();
    expect(screen.queryByLabelText('Image')).toBeNull();
    // Coming back must not reopen the editor.
    await user().click(rail().getByRole('button', { name: /^app\b/ }));
    expect(await screen.findByRole('tablist', { name: 'Service sections' })).toBeTruthy();
  });

  it('Open raw YAML editor from the editor lands on the compose editor', async () => {
    routes['PUT /api/stacks/web/services/app'] = { status: 422, body: { error: 'this change needs the raw YAML editor' } };
    mountDetail(h, '/stacks/web?service=app');
    await user().click(await screen.findByRole('button', { name: 'Edit app' }));
    await editImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await user().click(await screen.findByRole('button', { name: 'Save' }));
    await user().click(await screen.findByRole('button', { name: 'Open raw YAML editor' }));
    await user().click(within(await dialog()).getByRole('button', { name: 'Discard changes' })); // the form edit is still unsaved
    await waitFor(() => expect(here()).toBe('/stacks/web?tab=compose'));
    expect(await screen.findByRole('textbox', { name: 'Compose file' })).toBeTruthy();
    expect(get(unsavedChanges)).toBe(false);
  });
});

describe('removing a service from its editor', () => {
  it('a dirty editor asks to discard before Remove service opens its dialog', async () => {
    h.stackRes = { body: withCache() };
    mountDetail(h, '/stacks/web?service=cache');
    await user().click(await screen.findByRole('button', { name: 'Edit cache' }));
    await user().type(await screen.findByLabelText('Image'), 'x');
    await user().click(screen.getByRole('button', { name: 'Remove service cache' }));
    expect((await dialog()).textContent).toMatch(/unsaved/);
    expect(screen.queryByText('Remove service?')).toBeNull();
    await user().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Discard changes' }));
    expect(await screen.findByText('Remove cache from compose.yaml? Its container is removed on the next Up.')).toBeTruthy();
  });

  it('a 404 on remove still sends the page to the first remaining service', async () => {
    h.stackRes = { body: withCache() };
    routes['DELETE /api/stacks/web/services/cache?hash=h1'] = () => {
      h.stackRes = { body: stackDetail({ hash: 'h2', services: [appService()] }) };
      return { status: 404, body: { error: 'service not found' } };
    };
    mountDetail(h, '/stacks/web?service=cache');
    await user().click(await screen.findByRole('button', { name: 'Edit cache' }));
    await user().click(await screen.findByRole('button', { name: 'Remove service cache' }));
    await user().click(screen.getByRole('button', { name: 'Remove service' }));
    await waitFor(() => expect(here()).toBe('/stacks/web?service=app'));
    expect(screen.queryByLabelText('Image')).toBeNull();
  });

  it('soft-disables removal of the last service', async () => {
    h.stackRes = { body: stackDetail({ services: [appService()] }) };
    mountDetail(h, '/stacks/web?service=app');
    await user().click(await screen.findByRole('button', { name: 'Edit app' }));
    const b = await screen.findByRole('button', { name: 'Remove service app' });
    expect(b.getAttribute('aria-disabled')).toBe('true');
    expect(b.getAttribute('title')).toMatch(/last service/);
  });

  it('asks about next Up, reloads stale hash and requires a second confirmation', async () => {
    h.stackRes = { body: withCache() };
    let deletes = 0;
    routes['DELETE /api/stacks/web/services/cache?hash=h1'] = () => {
      deletes++;
      h.stackRes = { body: withCache('h2') };
      return { status: 409, body: { error: 'compose changed on disk' } };
    };
    routes['DELETE /api/stacks/web/services/cache?hash=h2'] = { body: stackDetail({ hash: 'h3', services: [appService()] }) };
    mountDetail(h, '/stacks/web?service=cache');
    await user().click(await screen.findByRole('button', { name: 'Edit cache' }));
    await user().click(await screen.findByRole('button', { name: 'Remove service cache' }));
    expect(screen.getByText('Remove cache from compose.yaml? Its container is removed on the next Up.')).toBeTruthy();
    await user().click(screen.getByRole('button', { name: 'Remove service' }));
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'The compose file changed and was reloaded. Confirm again to remove.');
    expect(deletes).toBe(1);
    await user().click(screen.getByRole('button', { name: 'Remove service' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    expect(screen.queryByRole('heading', { name: 'cache' })).toBeNull();
  });

  it('keeps dependency refusal visible and disables confirm', async () => {
    h.stackRes = { body: withCache() };
    routes['DELETE /api/stacks/web/services/cache?hash=h1'] = { status: 409, body: { error: 'app depends_on cache' } };
    mountDetail(h, '/stacks/web?service=cache');
    await user().click(await screen.findByRole('button', { name: 'Edit cache' }));
    await user().click(await screen.findByRole('button', { name: 'Remove service cache' }));
    await user().click(screen.getByRole('button', { name: 'Remove service' }));
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'app depends_on cache');
    expect(screen.getByRole('button', { name: 'Remove service' }).getAttribute('aria-disabled')).toBe('true');
  });

  it('remove ignores double confirm and a cancelled response cannot close the replacement dialog', async () => {
    h.stackRes = { body: withCache('h1', ['worker']) };
    let release!: () => void;
    const gate = new Promise<void>((resolve) => (release = resolve));
    routes['DELETE /api/stacks/web/services/cache?hash=h1'] = async () => {
      await gate;
      return { body: stackDetail({ hash: 'h2', services: [appService()] }) };
    };
    mountDetail(h, '/stacks/web?service=cache');
    const u = user();
    await u.click(await screen.findByRole('button', { name: 'Edit cache' }));
    await u.click(await screen.findByRole('button', { name: 'Remove service cache' }));
    await u.dblClick(screen.getByRole('button', { name: 'Remove service' }));
    expect(fake.calls.filter((c) => c.method === 'DELETE')).toHaveLength(1);
    expect(screen.getByRole('button', { name: 'Remove service' }).getAttribute('aria-busy')).toBe('true');
    await u.click(screen.getByRole('button', { name: 'Cancel' }));
    await u.click(rail().getByRole('button', { name: /^worker\b/ }));
    await u.click(await screen.findByRole('button', { name: 'Edit worker' }));
    await u.click(await screen.findByRole('button', { name: 'Remove service worker' }));
    const before = stackGets();
    release();
    await waitFor(() => expect(stackGets()).toBeGreaterThan(before));
    expect(screen.getByRole('alertdialog').textContent).toContain('Remove worker from compose.yaml?');
    expect(screen.getByRole('button', { name: 'Remove service' }).getAttribute('aria-busy')).toBeNull();
  });

  it.each(['last', 'missing', 'failed'] as const)('stale remove rechecks %s reload outcome before another confirmation', async (outcome) => {
    h.stackRes = { body: withCache() };
    routes['DELETE /api/stacks/web/services/cache?hash=h1'] = () => {
      h.stackRes =
        outcome === 'failed'
          ? { status: 500, body: { error: 'read failed' } }
          : { body: stackDetail({ hash: 'h2', services: outcome === 'last' ? [appService({ name: 'cache', containers: [] })] : [appService()] }) };
      return { status: 409, body: { error: 'compose changed on disk' } };
    };
    mountDetail(h, '/stacks/web?service=cache');
    await user().click(await screen.findByRole('button', { name: 'Edit cache' }));
    await user().click(await screen.findByRole('button', { name: 'Remove service cache' }));
    await user().click(screen.getByRole('button', { name: 'Remove service' }));
    await waitFor(() =>
      expect(within(screen.getByRole('alertdialog')).getByRole('alert').textContent).toMatch(outcome === 'last' ? /last service/ : outcome === 'missing' ? /no longer exists/ : /Could not reload.*read failed/),
    );
    expect(screen.getByRole('button', { name: 'Remove service' }).getAttribute('aria-disabled')).toBe(outcome === 'failed' ? null : 'true');
    expect(screen.getByRole('button', { name: 'Remove service' }).getAttribute('aria-busy')).toBeNull();
  });

  it('a pending detail read cannot overwrite an accepted remove result', async () => {
    h.stackRes = { body: withCache() };
    routes['DELETE /api/stacks/web/services/cache?hash=h1'] = { body: stackDetail({ hash: 'h2', services: [appService()] }) };
    mountDetail(h, '/stacks/web?service=cache');
    await screen.findByRole('heading', { name: 'cache' });
    let release!: () => void;
    const gate = new Promise<void>((resolve) => (release = resolve));
    routes['GET /api/stacks/web'] = async () => {
      await gate;
      return { body: withCache() };
    };
    await user().click(screen.getByRole('button', { name: 'Edit cache' }));
    const before = stackGets();
    startLive({ fetchStatus: () => fetch('/api/status').then((r) => r.json()) });
    await waitFor(() => expect(stackGets()).toBeGreaterThan(before));
    await user().click(await screen.findByRole('button', { name: 'Remove service cache' }));
    await user().click(screen.getByRole('button', { name: 'Remove service' }));
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'cache' })).toBeNull());
    release();
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(screen.queryByRole('heading', { name: 'cache' })).toBeNull();
  });

  it('a successful removal toasts, closes the editor and selects the first service', async () => {
    h.stackRes = { body: withCache() };
    routes['DELETE /api/stacks/web/services/cache?hash=h1'] = { body: stackDetail({ hash: 'h2', services: [appService()] }) };
    mountDetail(h, '/stacks/web?service=cache');
    await user().click(await screen.findByRole('button', { name: 'Edit cache' }));
    await user().click(await screen.findByRole('button', { name: 'Remove service cache' }));
    await user().click(screen.getByRole('button', { name: 'Remove service' }));
    await waitFor(() => expect(here()).toBe('/stacks/web?service=app'));
    expect(get(toasts).some((t) => t.message === 'Removed cache from compose.yaml. Run Up to apply.')).toBe(true);
    expect(screen.queryByLabelText('Image')).toBeNull();
    expect(await screen.findByRole('button', { name: 'Edit app' })).toBeTruthy();
    expect(get(unsavedChanges)).toBe(false);
  });
});
