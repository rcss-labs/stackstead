import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { get } from '$lib/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Operation, StackDetail as Detail } from '$lib/api/types';
import { changes, startLive, stopLive } from '$lib/stores/live';
import { unsavedChanges } from '$lib/stores/guard';
import { toasts } from '$lib/stores/toasts';
import { navigate, route } from '$lib/router';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream } from '../vitest-setup';

// Keep the plain textarea; the CodeMirror chunk has its own tests.
vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));

import StackDetail from './StackDetail.svelte';

const detail = (over: Partial<Detail> = {}): Detail => ({
  name: 'web',
  path: '/srv/stacks/web/compose.yaml',
  project: 'web',
  origin: 'managed',
  status: 'running',
  serviceCount: 1,
  running: 1,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
  hash: 'h1',
  services: [
    {
      name: 'app',
      config: { image: 'nginx:1', ports: ['80:80'], limits: {} },
      readOnly: [],
      containers: [{ id: 'abc', name: 'web-app-1', service: 'app', state: 'running', status: 'Up 2 minutes', health: 'none' }],
    },
  ],
  ...over,
});
const op = (over: Partial<Operation> = {}): Operation => ({
  id: 'op1',
  stack: 'web',
  action: 'up',
  state: 'running',
  startedAt: '2026-10-14T10:00:00Z',
  ...over,
});

let fake: FakeFetch;
let routes: Parameters<typeof installFakeFetch>[0];
let stream: FakeStream;
let stackRes: FakeResponse;
let stackGate: Promise<void> | undefined;
let opsRes: Operation[];
let statusRes: { docker: string; compose: string; events: string; metricsDegraded: boolean };

beforeEach(() => {
  toasts.reset();
  navigate('/stacks/web');
  stackRes = { body: detail() };
  stackGate = undefined;
  opsRes = [];
  statusRes = { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false };
  routes = {
    'GET /api/stacks/web': async () => {
      if (stackGate) await stackGate;
      return stackRes;
    },
    'GET /api/stacks/web/operations': () => ({ body: opsRes }),
    'GET /api/stacks/web/compose': { body: { content: 'services: {}\n', hash: 'h1' } },
    'GET /api/status': () => ({ body: statusRes }),
    'GET /api/stacks': { body: [] },
    'GET /api/operations?state=running': { body: [] },
    'GET /api/operations/op1': () => ({ body: { ...op(), lines: [] } }),
    'POST /api/stacks/web/services/app/preview': { body: { content: 'services: {}\n', hash: 'h1' } },
    'PUT /api/stacks/web/services/app': { body: { hash: 'h2' } },
    'POST /api/stacks/web/actions/up': { body: op() },
    'DELETE /api/stacks/web': { body: op({ id: 'rm1', action: 'down' }) },
    'GET /api/operations/rm1': { body: { ...op({ id: 'rm1', action: 'down' }), lines: [] } },
  };
  fake = installFakeFetch(routes);
  stream = installFakeStream({
    '/api/events': [],
    '/api/stacks/web/logs': [],
    '/api/stacks/web/metrics/live': [],
    '/api/operations/op1/stream': [],
    '/api/operations/rm1/stream': [],
  });
});
afterEach(() => {
  stopLive();
  stream.restore();
  fake.restore();
});

const stackGets = () => fake.calls.filter((c) => c.method === 'GET' && c.path === '/api/stacks/web').length;
const user = () => userEvent.setup();
const streamed = (suffix: string) => stream.requests.some((r) => r.path.startsWith(suffix));

describe('StackDetail', () => {
  it('loads the stack, shows header details and the five tabs with Services selected', async () => {
    render(StackDetail, { name: 'web' });
    expect(await screen.findByRole('heading', { name: 'web' })).toBeTruthy();
    expect(screen.getByText('managed')).toBeTruthy();
    expect(screen.getByText('/srv/stacks/web/compose.yaml')).toBeTruthy();
    const tabs = screen.getAllByRole('tab').map((t) => t.textContent?.replace(/\d+$/, ''));
    expect(tabs).toEqual(['Services', 'Compose', 'Logs', 'Metrics', 'Operations']);
    expect(screen.getByRole('tab', { name: /^Services/ }).getAttribute('aria-selected')).toBe('true');
    expect(screen.getByText('web-app-1')).toBeTruthy();
    expect(screen.getByText('nginx:1')).toBeTruthy();
  });

  it('?tab=logs mounts only the logs viewer', async () => {
    navigate('/stacks/web?tab=logs');
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    await waitFor(() => expect(streamed('/api/stacks/web/logs')).toBe(true));
    expect(screen.queryByText('web-app-1')).toBeNull();
    expect(fake.calls.some((c) => c.path === '/api/stacks/web/compose')).toBe(false);
    expect(streamed('/api/stacks/web/metrics/live')).toBe(false);
  });

  it('switching tabs unmounts the previous one and closes its stream', async () => {
    navigate('/stacks/web?tab=logs');
    render(StackDetail, { name: 'web' });
    await waitFor(() => expect(streamed('/api/stacks/web/logs')).toBe(true));
    const signal = stream.requests.find((r) => r.path.startsWith('/api/stacks/web/logs'))!.signal!;
    await user().click(screen.getByRole('tab', { name: /^Services/ }));
    expect(await screen.findByText('web-app-1')).toBeTruthy();
    expect(get(route).query.get('tab')).toBe('services');
    await waitFor(() => expect(signal.aborted).toBe(true));
  });

  it('refetches when a change for its project arrives or on resync, not for another project', async () => {
    startLive({ fetchStatus: () => fetch('/api/status').then((r) => r.json()) });
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    await waitFor(() => expect(get(changes)?.resync).toBe(true));
    await waitFor(() => expect(stackGets()).toBe(2)); // initial + resync on open
    stream.push('/api/events', 'event: change\ndata: {"project":"other","seq":1}\n\n');
    await new Promise((r) => setTimeout(r, 50));
    expect(stackGets()).toBe(2);
    stream.push('/api/events', 'event: change\ndata: {"project":"web","seq":2}\n\n');
    await waitFor(() => expect(stackGets()).toBe(3));
  });

  it('collapses a burst of changes for its project into at most two refetches of each endpoint', async () => {
    startLive({ fetchStatus: () => fetch('/api/status').then((r) => r.json()) });
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    await waitFor(() => expect(stackGets()).toBe(2)); // initial + resync on open
    const opsGets = () => fake.calls.filter((c) => c.path === '/api/stacks/web/operations').length;
    const [stackBefore, opsBefore] = [stackGets(), opsGets()];
    for (let i = 1; i <= 10; i++) stream.push('/api/events', `event: change\ndata: {"project":"web","seq":${i}}\n\n`);
    await new Promise((r) => setTimeout(r, 1500));
    expect(stackGets() - stackBefore).toBeGreaterThanOrEqual(1);
    expect(stackGets() - stackBefore).toBeLessThanOrEqual(2);
    expect(opsGets() - opsBefore).toBeGreaterThanOrEqual(1);
    expect(opsGets() - opsBefore).toBeLessThanOrEqual(2);
  });

  it('shows the not-found view with a way back on 404', async () => {
    stackRes = { status: 404, body: { error: 'stack not found' } };
    render(StackDetail, { name: 'web' });
    expect(await screen.findByText(/not found/i)).toBeTruthy();
    expect(screen.getByRole('link', { name: /stacks/i }).getAttribute('href')).toBe('/');
    expect(screen.queryByRole('tablist')).toBeNull();
  });

  it('a parse error switches to raw-only mode: compose and operations tabs, message shown', async () => {
    stackRes = { body: detail({ status: 'unavailable', parseError: 'yaml: line 3: mapping values are not allowed', services: [] }) };
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    expect(screen.getAllByRole('tab').map((t) => t.textContent?.replace(/\d+$/, ''))).toEqual(['Compose', 'Operations']);
    expect(screen.getByRole('tab', { name: 'Compose' }).getAttribute('aria-selected')).toBe('true');
    expect(screen.getAllByText(/mapping values are not allowed/).length).toBeGreaterThan(0);
    expect(await screen.findByRole('textbox', { name: /compose file/i })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Edit app' })).toBeNull();
  });

  it('a missing compose file shows the message and no editor', async () => {
    stackRes = { body: detail({ status: 'unavailable', parseError: 'open /srv/stacks/web/compose.yaml: no such file or directory', services: [] }) };
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    expect(screen.getAllByText(/no such file/).length).toBeGreaterThan(0);
    expect(screen.queryByRole('textbox', { name: /compose file/i })).toBeNull();
  });

  it('Up starts the action and opens the operations tab streaming it', async () => {
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    await user().click(screen.getByRole('button', { name: 'Up' }));
    await waitFor(() => expect(get(route).query.get('tab')).toBe('operations'));
    expect(get(route).query.get('op')).toBe('op1');
    await waitFor(() => expect(streamed('/api/operations/op1/stream')).toBe(true));
  });

  it('a 409 on an action toasts busy and a 503 refreshes status', async () => {
    fake.restore();
    fake = installFakeFetch({
      'GET /api/stacks/web': { body: detail() },
      'GET /api/stacks/web/operations': { body: [] },
      'POST /api/stacks/web/actions/up': { status: 409, body: { error: 'operation already running' } },
      'POST /api/stacks/web/actions/pull': { status: 503, body: { error: 'docker unavailable' } },
      'GET /api/status': { body: statusRes },
    });
    startLive({ fetchStatus: () => fetch('/api/status').then((r) => r.json()) });
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    await user().click(screen.getByRole('button', { name: 'Up' }));
    await waitFor(() => expect(get(toasts).some((t) => /already running/.test(t.message))).toBe(true));
    const before = fake.calls.filter((c) => c.path === '/api/status').length;
    await user().click(screen.getByRole('button', { name: 'Pull' }));
    await waitFor(() => expect(fake.calls.filter((c) => c.path === '/api/status').length).toBeGreaterThan(before));
    expect(get(toasts).some((t) => /docker unavailable/.test(t.message))).toBe(true);
  });

  it('lifecycle buttons are soft-disabled with a reason while an operation runs', async () => {
    opsRes = [op()];
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Up' }).getAttribute('aria-disabled')).toBe('true'));
  });

  it('lifecycle is disabled with a reason when compose is unavailable', async () => {
    statusRes = { ...statusRes, compose: 'unavailable' };
    startLive({ fetchStatus: () => fetch('/api/status').then((r) => r.json()) });
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Up' }).getAttribute('aria-disabled')).toBe('true'));
  });

  it('removing a stack streams the operation and navigates home with a toast on success', async () => {
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    await user().click(screen.getByRole('button', { name: 'Remove' }));
    const dlg = await screen.findByRole('alertdialog');
    await user().click(within(dlg).getByRole('button', { name: 'Remove' }));
    await waitFor(() => expect(streamed('/api/operations/rm1/stream')).toBe(true));
    stream.push(
      '/api/operations/rm1/stream',
      'event: done\ndata: {"id":"rm1","stack":"web","action":"down","state":"succeeded","startedAt":"2026-10-14T10:00:00Z","exitCode":0}\n\n',
    );
    await waitFor(() => expect(get(route).path).toBe('/'));
    expect(get(toasts).some((t) => t.kind === 'success')).toBe(true);
  });

  it('?op= preselects an operation in the operations tab and lists recent ones', async () => {
    opsRes = [op({ state: 'succeeded', endedAt: '2026-10-14T10:01:00Z', exitCode: 0 }), op({ id: 'op0', action: 'down', state: 'failed' })];
    navigate('/stacks/web?tab=operations&op=op1');
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    await waitFor(() => expect(streamed('/api/operations/op1/stream')).toBe(true));
    expect(await screen.findByText('failed')).toBeTruthy();
  });

  it('asks before leaving the compose tab with unsaved edits, and stays when cancelled', async () => {
    navigate('/stacks/web?tab=compose');
    render(StackDetail, { name: 'web' });
    const ta = (await screen.findByRole('textbox', { name: /compose file/i })) as HTMLTextAreaElement;
    await waitFor(() => expect(ta.value).toBe('services: {}\n'));
    await user().type(ta, '# edit');
    await user().click(screen.getByRole('tab', { name: 'Logs' }));
    const dlg = await screen.findByRole('alertdialog');
    expect(dlg.textContent).toMatch(/unsaved/i);
    await user().click(within(dlg).getByRole('button', { name: 'Cancel' }));
    expect(get(route).query.get('tab')).toBe('compose');
    expect(screen.getByRole('textbox', { name: /compose file/i })).toBeTruthy();
    await user().click(screen.getByRole('tab', { name: 'Logs' }));
    await user().click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Discard changes' }));
    await waitFor(() => expect(get(route).query.get('tab')).toBe('logs'));
  });

  it('switching tabs with a clean editor does not ask', async () => {
    navigate('/stacks/web?tab=compose');
    render(StackDetail, { name: 'web' });
    await screen.findByRole('textbox', { name: /compose file/i });
    await user().click(screen.getByRole('tab', { name: 'Logs' }));
    expect(screen.queryByRole('alertdialog')).toBeNull();
    await waitFor(() => expect(get(route).query.get('tab')).toBe('logs'));
  });

  it('the service editor is mounted from Edit and keeps its state when the stack refetches', async () => {
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    await user().click(screen.getByRole('button', { name: 'Edit app' }));
    const image = (await screen.findByLabelText('Image')) as HTMLInputElement;
    await user().clear(image);
    await user().type(image, 'nginx:2');
    stackRes = { body: detail({ hash: 'h2' }) };
    startLive({ fetchStatus: () => fetch('/api/status').then((r) => r.json()) });
    await waitFor(() => expect(stackGets()).toBeGreaterThan(1));
    expect((screen.getByLabelText('Image') as HTMLInputElement).value).toBe('nginx:2');
  });

  it('history navigation away from a dirty compose editor does not leave a stale unsaved flag', async () => {
    navigate('/stacks/web?tab=compose');
    render(StackDetail, { name: 'web' });
    const ta = (await screen.findByRole('textbox', { name: /compose file/i })) as HTMLTextAreaElement;
    await waitFor(() => expect(ta.value).toBe('services: {}\n'));
    await user().type(ta, '# edit');
    await waitFor(() => expect(get(unsavedChanges)).toBe(true));
    window.history.replaceState(null, '', '/stacks/web?tab=services');
    window.dispatchEvent(new PopStateEvent('popstate'));
    navigate('/stacks/web?tab=services', { replace: true });
    await screen.findByRole('button', { name: 'Edit app' });
    await waitFor(() => expect(get(unsavedChanges)).toBe(false));
    await user().click(screen.getByRole('tab', { name: 'Logs' }));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('a 404 after a remove operation was started counts as a successful removal', async () => {
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    await user().click(screen.getByRole('button', { name: 'Remove' }));
    await user().click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Remove' }));
    await waitFor(() => expect(streamed('/api/operations/rm1/stream')).toBe(true));
    stackRes = { status: 404, body: { error: 'stack not found' } };
    startLive({ fetchStatus: () => fetch('/api/status').then((r) => r.json()) });
    await waitFor(() => expect(get(route).path).toBe('/'));
    expect(get(toasts).filter((t) => t.kind === 'success')).toHaveLength(1);
  });

  describe('v2 layout', () => {
    const liveRequests = () => stream.requests.filter((r) => r.path.startsWith('/api/stacks/web/metrics/live'));
    const wide = () => vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} }));
    afterEach(() => vi.unstubAllGlobals());

    it('aside hidden below 1100 px', async () => {
      render(StackDetail, { name: 'web' });
      await screen.findByRole('heading', { name: 'web' });
      expect(screen.queryByRole('complementary', { name: 'Stack summary' })).toBeNull();
    });

    it('aside shown at 1100 px and wider', async () => {
      wide();
      render(StackDetail, { name: 'web' });
      expect(await screen.findByRole('complementary', { name: 'Stack summary' })).toBeTruthy();
    });

    it('no live stream when narrow and not on Metrics', async () => {
      render(StackDetail, { name: 'web' });
      await screen.findByRole('heading', { name: 'web' });
      await new Promise((r) => setTimeout(r, 30));
      expect(liveRequests()).toHaveLength(0);
    });

    it('one stream shared by aside and Metrics tab', async () => {
      wide();
      render(StackDetail, { name: 'web' });
      await screen.findByRole('complementary', { name: 'Stack summary' });
      await waitFor(() => expect(liveRequests()).toHaveLength(1));
      await user().click(screen.getByRole('tab', { name: 'Metrics' }));
      expect(await screen.findByRole('region', { name: 'Resource metrics' })).toBeTruthy();
      await new Promise((r) => setTimeout(r, 30));
      expect(liveRequests()).toHaveLength(1);
      expect(liveRequests()[0].signal!.aborted).toBe(false);
    });

    it('stream closes when leaving both', async () => {
      navigate('/stacks/web?tab=metrics');
      render(StackDetail, { name: 'web' });
      await waitFor(() => expect(liveRequests()).toHaveLength(1));
      const signal = liveRequests()[0].signal!;
      await user().click(await screen.findByRole('tab', { name: /^Services/ }));
      await waitFor(() => expect(signal.aborted).toBe(true));
    });

    it('stream closes when the page is destroyed', async () => {
      wide();
      const { unmount } = render(StackDetail, { name: 'web' });
      await waitFor(() => expect(liveRequests()).toHaveLength(1));
      const signal = liveRequests()[0].signal!;
      unmount();
      await waitFor(() => expect(signal.aborted).toBe(true));
    });

    it('Logs button preselects the service', async () => {
      render(StackDetail, { name: 'web' });
      await user().click(await screen.findByRole('link', { name: 'Logs' }));
      expect(get(route).query.get('tab')).toBe('logs');
      expect(get(route).query.get('service')).toBe('app');
      expect(((await screen.findByLabelText('Service')) as HTMLSelectElement).value).toBe('app');
    });

    it('the Logs button asks before discarding an open service editor', async () => {
      stackRes = {
        body: detail({
          serviceCount: 2,
          services: [
            ...detail().services,
            { name: 'db', config: { image: 'pg:1', limits: {} }, readOnly: [], containers: [] },
          ],
        }),
      };
      render(StackDetail, { name: 'web' });
      await user().click(await screen.findByRole('button', { name: 'Edit app' }));
      const image = (await screen.findByLabelText('Image')) as HTMLInputElement;
      await user().type(image, 'x');
      await user().click(screen.getByRole('link', { name: 'Logs' }));
      expect(await screen.findByRole('alertdialog')).toBeTruthy();
      expect(get(route).query.get('tab')).toBeNull();
    });

    it('no live stream while the stack is loading, failed to load, or not found', async () => {
      wide();
      let release!: () => void;
      stackGate = new Promise<void>((r) => (release = r));
      const first = render(StackDetail, { name: 'web' });
      await new Promise((r) => setTimeout(r, 30));
      expect(liveRequests()).toHaveLength(0);
      release();
      await screen.findByRole('complementary', { name: 'Stack summary' });
      first.unmount();
      const before = liveRequests().length;

      stackGate = undefined;
      stackRes = { status: 500, body: { error: 'boom' } };
      const failing = render(StackDetail, { name: 'web' });
      await screen.findByText(/Could not load the stack/);
      await new Promise((r) => setTimeout(r, 30));
      expect(liveRequests().length).toBe(before);
      failing.unmount();

      stackRes = { status: 404, body: { error: 'stack not found' } };
      render(StackDetail, { name: 'web' });
      await screen.findByText(/not found/i);
      await new Promise((r) => setTimeout(r, 30));
      expect(liveRequests().length).toBe(before);
    });

    it('opens one stream when the window widens and closes it when it narrows again', async () => {
      const listeners = new Set<(e: { matches: boolean }) => void>();
      vi.stubGlobal('matchMedia', () => ({
        matches: false,
        addEventListener: (_: string, fn: (e: { matches: boolean }) => void) => listeners.add(fn),
        removeEventListener: (_: string, fn: (e: { matches: boolean }) => void) => listeners.delete(fn),
      }));
      render(StackDetail, { name: 'web' });
      await screen.findByRole('heading', { name: 'web' });
      expect(liveRequests()).toHaveLength(0);
      listeners.forEach((fn) => fn({ matches: true }));
      await waitFor(() => expect(liveRequests()).toHaveLength(1));
      const signal = liveRequests()[0].signal!;
      listeners.forEach((fn) => fn({ matches: false }));
      await waitFor(() => expect(signal.aborted).toBe(true));
      expect(liveRequests()).toHaveLength(1);
    });

    it('the service editor Apply is not soft-disabled by a running operation of the page', async () => {
      opsRes = [op()];
      render(StackDetail, { name: 'web' });
      await user().click(await screen.findByRole('button', { name: 'Edit app' }));
      const image = (await screen.findByLabelText('Image')) as HTMLInputElement;
      await user().clear(image);
      await user().type(image, 'nginx:2');
      await user().click(screen.getByRole('button', { name: 'Preview changes' }));
      await screen.findByTestId('diff-view');
      await user().click(screen.getByRole('button', { name: 'Save' }));
      const apply = await screen.findByRole('button', { name: 'Apply' });
      expect(apply.getAttribute('aria-disabled')).toBeNull();
      expect(screen.getByRole('button', { name: 'Up' }).getAttribute('aria-disabled')).toBe('true');
    });

    it('tab badges show service and operation counts', async () => {
      opsRes = [op({ state: 'succeeded' }), op({ id: 'op0', state: 'failed' })];
      render(StackDetail, { name: 'web' });
      expect(await screen.findByRole('tab', { name: 'Services, 1' })).toBeTruthy();
      expect(await screen.findByRole('tab', { name: 'Operations, 2' })).toBeTruthy();
    });

    it('copy path button', async () => {
      render(StackDetail, { name: 'web' });
      expect(await screen.findByRole('button', { name: 'Copy path' })).toBeTruthy();
    });
  });
});

describe('service membership', () => {
  function twoServices() {
    const d = detail();
    return detail({ serviceCount: 2, services: [...d.services, { ...d.services[0], name: 'cache', config: { image: 'redis:7', limits: {} }, containers: [] }] });
  }
  it('opens add, guards a dirty panel before editing, and hides Add during editing', async () => {
    render(StackDetail, { name: 'web' });
    await user().click(await screen.findByRole('button', { name: 'Add service' }));
    await user().type(await screen.findByLabelText('Service name'), 'cache');
    expect(get(unsavedChanges)).toBe(true);
    await user().click(screen.getByRole('button', { name: 'Edit app' }));
    await user().click(await screen.findByRole('button', { name: 'Discard changes' }));
    await screen.findByLabelText('Image');
    expect(screen.queryByLabelText('Service name')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Add service' })).toBeNull();
    expect(get(unsavedChanges)).toBe(false);
  });
  it('does not offer Add on parse errors', async () => {
    stackRes = { body: detail({ parseError: 'bad YAML' }) };
    render(StackDetail, { name: 'web' });
    await screen.findByRole('heading', { name: 'web' });
    expect(screen.queryByRole('button', { name: 'Add service' })).toBeNull();
  });
  it('adds reviewed service, replaces detail, closes panel and gives next-Up guidance', async () => {
    routes['GET /api/networks'] = { body: [] };
    routes['POST /api/stacks/web/services/preview'] = { body: { content: 'new', hash: 'h1' } };
    routes['POST /api/stacks/web/services'] = { body: twoServices(), headers: { 'X-Stackstead-Checks': 'partial' } };
    render(StackDetail, { name: 'web' });
    await user().click(await screen.findByRole('button', { name: 'Add service' }));
    await user().type(await screen.findByLabelText('Service name'), 'cache');
    await user().type(screen.getByLabelText('Image'), 'redis:7');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await user().click(await screen.findByRole('button', { name: 'Save' }));
    expect(await screen.findByRole('heading', { name: 'cache' })).toBeTruthy();
    expect(screen.queryByLabelText('Service name')).toBeNull();
    expect(get(toasts).some(t => t.message === 'Added cache to compose.yaml. Run Up to start it.')).toBe(true);
  });
  it('soft-disables removal of the last service', async () => {
    render(StackDetail, { name: 'web' });
    const b = await screen.findByRole('button', { name: 'Remove service app' });
    expect(b.getAttribute('aria-disabled')).toBe('true');
    expect(b.getAttribute('title')).toMatch(/last service/);
  });
  it('asks about next Up, reloads stale hash and requires a second confirmation', async () => {
    stackRes = { body: twoServices() };
    let calls = 0;
    routes['DELETE /api/stacks/web/services/cache?hash=h1'] = () => {
      calls++;
      stackRes = { body: { ...twoServices(), hash: 'h2' } };
      return { status: 409, body: { error: 'compose changed on disk' } };
    };
    routes['DELETE /api/stacks/web/services/cache?hash=h2'] = { body: detail({ hash: 'h3' }) };
    render(StackDetail, { name: 'web' });
    await user().click(await screen.findByRole('button', { name: 'Remove service cache' }));
    expect(screen.getByText('Remove cache from compose.yaml? Its container is removed on the next Up.')).toBeTruthy();
    await user().click(screen.getByRole('button', { name: 'Remove service' }));
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'The compose file changed and was reloaded. Confirm again to remove.');
    expect(calls).toBe(1);
    await user().click(screen.getByRole('button', { name: 'Remove service' }));
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    expect(screen.queryByRole('heading', { name: 'cache' })).toBeNull();
  });
  it('keeps dependency refusal visible and disables confirm', async () => {
    stackRes = { body: twoServices() };
    routes['DELETE /api/stacks/web/services/cache?hash=h1'] = { status: 409, body: { error: 'app depends_on cache' } };
    render(StackDetail, { name: 'web' });
    await user().click(await screen.findByRole('button', { name: 'Remove service cache' }));
    await user().click(screen.getByRole('button', { name: 'Remove service' }));
    expect(await screen.findByRole('alert')).toHaveProperty('textContent', 'app depends_on cache');
    expect(screen.getByRole('button', { name: 'Remove service' }).getAttribute('aria-disabled')).toBe('true');
  });
});
it('remove ignores double confirm and a cancelled response cannot close the replacement dialog', async () => {
  const d = detail();
  stackRes = { body: detail({ serviceCount: 3, services: [...d.services, { ...d.services[0], name: 'cache' }, { ...d.services[0], name: 'worker' }] }) };
  let release!: () => void;
  const gate = new Promise<void>(resolve => release = resolve);
  routes['DELETE /api/stacks/web/services/cache?hash=h1'] = async () => {
    await gate;
    return { body: detail({ hash: 'h2' }) };
  };
  render(StackDetail, { name: 'web' });
  const u = user();
  await u.click(await screen.findByRole('button', { name: 'Remove service cache' }));
  await u.dblClick(screen.getByRole('button', { name: 'Remove service' }));
  expect(fake.calls.filter(c => c.method === 'DELETE')).toHaveLength(1);
  expect(screen.getByRole('button', { name: 'Remove service' }).getAttribute('aria-busy')).toBe('true');
  await u.click(screen.getByRole('button', { name: 'Cancel' }));
  await u.click(screen.getByRole('button', { name: 'Remove service worker' }));
  const before = stackGets();
  release();
  await waitFor(() => expect(stackGets()).toBeGreaterThan(before));
  expect(screen.getByRole('alertdialog').textContent).toContain('Remove worker from compose.yaml?');
  expect(screen.getByRole('button', { name: 'Remove service' }).getAttribute('aria-busy')).toBeNull();
});
it.each(['last', 'missing', 'failed'] as const)('stale remove rechecks %s reload outcome before another confirmation', async (outcome) => {
  const d = detail();
  stackRes = { body: detail({ serviceCount: 2, services: [...d.services, { ...d.services[0], name: 'cache' }] }) };
  routes['DELETE /api/stacks/web/services/cache?hash=h1'] = () => {
    stackRes = outcome === 'failed' ? { status: 500, body: { error: 'read failed' } } : { body: detail({ hash: 'h2', services: outcome === 'last' ? [{ ...d.services[0], name: 'cache' }] : d.services }) };
    return { status: 409, body: { error: 'compose changed on disk' } };
  };
  render(StackDetail, { name: 'web' });
  await user().click(await screen.findByRole('button', { name: 'Remove service cache' }));
  await user().click(screen.getByRole('button', { name: 'Remove service' }));
  const alert = await screen.findByRole('alert');
  expect(alert.textContent).toMatch(outcome === 'last' ? /last service/ : outcome === 'missing' ? /no longer exists/ : /Could not reload.*read failed/);
  expect(screen.getByRole('button', { name: 'Remove service' }).getAttribute('aria-disabled')).toBe(outcome === 'failed' ? null : 'true');
  expect(screen.getByRole('button', { name: 'Remove service' }).getAttribute('aria-busy')).toBeNull();
});
it('a pending detail read cannot overwrite an accepted remove result', async () => {
  const initial = detail();
  const two = detail({ serviceCount: 2, services: [...initial.services, { ...initial.services[0], name: 'cache' }] });
  stackRes = { body: two };
  routes['DELETE /api/stacks/web/services/cache?hash=h1'] = { body: detail({ hash: 'h2' }) };
  render(StackDetail, { name: 'web' });
  await screen.findByRole('heading', { name: 'cache' });
  let release!: () => void;
  const gate = new Promise<void>(resolve => release = resolve);
  routes['GET /api/stacks/web'] = async () => {
    await gate;
    return { body: two };
  };
  const before = stackGets();
  startLive({ fetchStatus: () => fetch('/api/status').then(r => r.json()) });
  await waitFor(() => expect(stackGets()).toBeGreaterThan(before));
  await user().click(screen.getByRole('button', { name: 'Remove service cache' }));
  await user().click(screen.getByRole('button', { name: 'Remove service' }));
  await waitFor(() => expect(screen.queryByRole('heading', { name: 'cache' })).toBeNull());
  release();
  await new Promise(resolve => setTimeout(resolve, 20));
  expect(screen.queryByRole('heading', { name: 'cache' })).toBeNull();
});
