import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { unsavedChanges } from '$lib/stores/guard';
import { startLive } from '$lib/stores/live';
import { installFakeFetch, installFakeStream } from '../../vitest-setup';

// Keep the plain textarea: the CodeMirror chunk is exercised in the YamlEditor and DiffView tests.
vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));

import { App } from '../App';
import { installHarness, mountDetail, removeHarness, stackDetail, type Harness } from './detailTestKit';

let h: Harness;
let onPut: (() => void) | undefined;
beforeEach(() => {
  onPut = undefined;
  h = installHarness();
  h.composeRes = { body: { content: 'services: {}\n', hash: 'c1' } };
  h.stream.restore(); // unwind in reverse order of installation
  h.fake.restore();
  h.fake = installFakeFetch({
    'GET /api/stacks/web': () => h.stackRes,
    'GET /api/stacks/web/operations': () => ({ body: h.operations }),
    'GET /api/stacks/web/health?span=60m': { body: { from: 0, to: 3600, services: [] } },
    'GET /api/stacks/web/metrics': { body: { points: [] } },
    'GET /api/stacks/web/compose': () => h.composeRes,
    'PUT /api/stacks/web/compose': () => {
      onPut?.();
      return { body: { hash: 'c2' } };
    },
    'POST /api/stacks/web/actions/up': { body: h.operations[0] },
    'GET /api/status': { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
    'GET /api/stacks': { body: [] },
    'GET /api/operations?state=running': { body: [] },
    'GET /api/me': { body: { username: 'admin' } },
    'GET /api/setup/status': { body: { needsSetup: false } },
    'GET /api/host': { body: {} },
  });
  // The wider fake fetch replaced the one the stream fake wraps, so the stream fake is installed again on top.
  h.stream = installFakeStream({
    '/api/events': [],
    '/api/stacks/web/logs': [],
    '/api/stacks/web/metrics/live': [],
    '/api/operations/op1/stream': [],
  });
});
afterEach(async () => {
  cleanup();
  await removeHarness(h);
});

const user = () => userEvent.setup();
const here = () => screen.getByTestId('location').textContent;
const rail = () => within(screen.getByRole('navigation', { name: 'Services' }));
const textbox = () => screen.getByRole('textbox', { name: 'Compose file' }) as HTMLTextAreaElement;
const edit = (v: string) => fireEvent.change(textbox(), { target: { value: v } });

async function openDirtyEditor() {
  h.operations = []; // a running operation would soft-disable the header actions
  mountDetail(h, '/stacks/web?tab=compose');
  await user().click(await screen.findByRole('button', { name: 'Edit YAML' }));
  await waitFor(() => expect(textbox().value).toBe('services: {}\n'));
  edit('services: {}\n# edit\n');
  await waitFor(() => expect(get(unsavedChanges)).toBe(true));
}
const dialog = () => screen.findByRole('alertdialog', { name: 'Discard unsaved changes?' });
const cancelDialog = async () => {
  await user().click(within(await dialog()).getByRole('button', { name: 'Cancel' }));
  await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
};

describe('compose editor on the stack page', () => {
  it('Edit YAML opens the editor on the compose item and Apply runs up', async () => {
    mountDetail(h, '/stacks/web?tab=compose');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    expect(screen.queryByRole('textbox', { name: 'Compose file' })).toBeNull();
    await user().click(await screen.findByRole('button', { name: 'Edit YAML' }));
    await waitFor(() => expect(textbox().value).toBe('services: {}\n'));
    expect(screen.queryByRole('button', { name: 'Edit YAML' })).toBeNull();
    edit('services: {}\n# x\n');
    await user().click(screen.getByRole('button', { name: 'Save' }));
    await user().click(await screen.findByRole('button', { name: 'Apply' }));
    await waitFor(() => expect(h.calls('POST', '/api/stacks/web/actions/up')).toBe(1));
    await waitFor(() => expect(here()).toBe('/stacks/web?tab=operations&op=op1'));
  });

  it('a parse error opens the raw editor directly and hides Edit YAML', async () => {
    h.stackRes = { body: stackDetail({ status: 'unavailable', parseError: 'yaml: line 3: mapping values are not allowed', services: [] }) };
    mountDetail(h, '/stacks/web');
    expect(await screen.findByRole('textbox', { name: 'Compose file' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Edit YAML' })).toBeNull();
    expect(rail().queryByRole('button', { name: /^app\b/ })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Edit app' })).toBeNull();
  });

  it('saving a fixed file on a parse-error stack keeps the editor and offers Apply', async () => {
    h.stackRes = { body: stackDetail({ status: 'unavailable', parseError: 'yaml: line 3: bad', services: [] }) };
    onPut = () => (h.stackRes = { body: stackDetail() });
    mountDetail(h, '/stacks/web');
    await waitFor(() => expect(textbox().value).toBe('services: {}\n'));
    edit('services: {}\n# fixed\n');
    await user().click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByRole('button', { name: 'Apply' })).toBeTruthy();
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull()); // the page has refetched and parsed
    await new Promise((r) => setTimeout(r, 200));
    expect(textbox()).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Apply' })).toBeTruthy();
  });

  it('a refetch that clears the parse error keeps a dirty draft in the editor', async () => {
    h.stackRes = { body: stackDetail({ status: 'unavailable', parseError: 'yaml: line 3: bad', services: [] }) };
    mountDetail(h, '/stacks/web');
    await waitFor(() => expect(textbox().value).toBe('services: {}\n'));
    edit('services: {}\n# mine\n');
    startLive({ fetchStatus: () => fetch('/api/status').then((r) => r.json()) });
    await waitFor(() => expect(h.stream.requests.some((r) => r.path.startsWith('/api/events'))).toBe(true));
    h.stackRes = { body: stackDetail() }; // fixed on disk meanwhile
    h.stream.push('/api/events', 'event: change\ndata: {"project":"web","seq":1}\n\n');
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
    expect(textbox().value).toBe('services: {}\n# mine\n');
    expect(get(unsavedChanges)).toBe(true);
  });

  it('shows one parse-error alert, the page banner', async () => {
    h.stackRes = { body: stackDetail({ status: 'unavailable', parseError: 'yaml: line 3: bad', services: [] }) };
    mountDetail(h, '/stacks/web');
    await screen.findByRole('textbox', { name: 'Compose file' });
    expect(screen.getAllByRole('alert')).toHaveLength(1);
  });

  it('leaving the compose item clears the dirty flag before the editor is reopened', async () => {
    await openDirtyEditor();
    const seen: boolean[] = [];
    const stop = unsavedChanges.subscribe((v) => seen.push(v));
    h.goTo('/stacks/web?tab=operations');
    await waitFor(() => expect(get(unsavedChanges)).toBe(false));
    seen.length = 0;
    h.goTo('/stacks/web?tab=compose');
    await user().click(await screen.findByRole('button', { name: 'Edit YAML' }));
    await screen.findByRole('textbox', { name: 'Compose file' });
    stop();
    expect(seen).not.toContain(true);
  });

  it('a missing compose file shows the message and no editor', async () => {
    h.stackRes = { body: stackDetail({ status: 'unavailable', parseError: 'open /srv/stacks/web/compose.yaml: no such file or directory', services: [] }) };
    mountDetail(h, '/stacks/web');
    const banner = await screen.findByRole('alert');
    expect(banner.textContent).toMatch(/no such file/);
    expect(screen.queryByRole('textbox', { name: 'Compose file' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Edit YAML' })).toBeNull();
  });

  it('asks before leaving the compose editor with unsaved edits, and stays when cancelled', async () => {
    await openDirtyEditor();
    await user().click(rail().getByRole('button', { name: /^Operations/ }));
    expect((await dialog()).textContent).toMatch(/unsaved/);
    await user().click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Cancel' }));
    expect(here()).toBe('/stacks/web?tab=compose');
    expect(textbox().value).toBe('services: {}\n# edit\n');
    await user().click(rail().getByRole('button', { name: /^Operations/ }));
    await user().click(within(await dialog()).getByRole('button', { name: 'Discard changes' }));
    await waitFor(() => expect(here()).toBe('/stacks/web?tab=operations'));
    await waitFor(() => expect(get(unsavedChanges)).toBe(false));
  });

  it('a dirty draft asks before a header action, a timeline name, a chip and a rail service too', async () => {
    await openDirtyEditor();
    const targets = [
      () => screen.getByRole('button', { name: 'Up' }),
      () => within(screen.getByRole('region', { name: 'Health · last 60 min' })).getByRole('button', { name: /^app/ }),
      () => within(screen.getByRole('navigation', { name: 'Service chips' })).getByRole('button', { name: /^app/ }),
      () => rail().getByRole('button', { name: /^app\b/ }),
    ];
    for (const target of targets) {
      await user().click(target());
      await dialog();
      await cancelDialog();
    }
    expect(here()).toBe('/stacks/web?tab=compose');
    expect(h.calls('POST', '/api/stacks/web/actions/up')).toBe(0);
  });

  it('console actions never ask, even with a dirty draft', async () => {
    await openDirtyEditor();
    const filter = within(await screen.findByRole('group', { name: 'Service filter' }));
    await user().click(filter.getByRole('button', { name: 'db' }));
    await user().click(screen.getByRole('button', { name: 'Logs' }));
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(get(unsavedChanges)).toBe(true);
    expect(here()).toBe('/stacks/web?tab=compose');
  });

  it('switching items with a clean editor does not ask', async () => {
    mountDetail(h, '/stacks/web?tab=compose');
    await user().click(await screen.findByRole('button', { name: 'Edit YAML' }));
    await waitFor(() => expect(textbox().value).toBe('services: {}\n'));
    await user().click(rail().getByRole('button', { name: /^Operations/ }));
    expect(screen.queryByRole('alertdialog')).toBeNull();
    await waitFor(() => expect(here()).toBe('/stacks/web?tab=operations'));
  });

  it('a link click outside the pane asks through the App guard', async () => {
    window.history.replaceState(null, '', '/stacks/web?tab=compose');
    render(<App />);
    await user().click(await screen.findByRole('button', { name: 'Edit YAML' }));
    await waitFor(() => expect(textbox().value).toBe('services: {}\n'));
    edit('services: {}\n# edit\n');
    await waitFor(() => expect(get(unsavedChanges)).toBe(true));
    await user().click(within(screen.getByRole('navigation', { name: 'Main' })).getByRole('link', { name: 'Containers' }));
    const dlg = await dialog();
    expect(window.location.pathname).toBe('/stacks/web');
    await user().click(within(dlg).getByRole('button', { name: 'Cancel' }));
    expect(window.location.pathname).toBe('/stacks/web');
    expect(textbox().value).toBe('services: {}\n# edit\n');
    await user().click(within(screen.getByRole('navigation', { name: 'Main' })).getByRole('link', { name: 'Containers' }));
    await user().click(within(await dialog()).getByRole('button', { name: 'Discard changes' }));
    await waitFor(() => expect(window.location.pathname).toBe('/containers'));
    expect(get(unsavedChanges)).toBe(false);
  });

  it('history navigation away from a dirty compose editor does not leave a stale unsaved flag', async () => {
    await openDirtyEditor();
    h.goTo('/stacks/web?service=app');
    await waitFor(() => expect(get(unsavedChanges)).toBe(false));
    await user().click(await screen.findByRole('tab', { name: 'Metrics' }));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('leaving the compose item exits edit mode', async () => {
    await openDirtyEditor();
    await user().click(rail().getByRole('button', { name: /^Operations/ }));
    await user().click(within(await dialog()).getByRole('button', { name: 'Discard changes' }));
    await waitFor(() => expect(here()).toBe('/stacks/web?tab=operations'));
    await user().click(rail().getByRole('button', { name: /^Compose file/ }));
    expect(await screen.findByRole('button', { name: 'Edit YAML' })).toBeTruthy();
    expect(screen.queryByRole('textbox', { name: 'Compose file' })).toBeNull();
    expect(get(unsavedChanges)).toBe(false);
  });
});
