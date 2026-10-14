import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Operation, StackDetail as Detail } from '$lib/api/types';
import { changes, startLive, stopLive } from '$lib/stores/live';
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
  hash: 'h1',
  services: [
    {
      name: 'app',
      config: { image: 'nginx:1', ports: ['80:80'], limits: {} },
      readOnly: [],
      containers: [{ id: 'abc', name: 'web-app-1', service: 'app', state: 'running', status: 'Up 2 minutes' }],
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
let stream: FakeStream;
let stackRes: FakeResponse;
let opsRes: Operation[];
let statusRes: { docker: string; compose: string; events: string; metricsDegraded: boolean };

beforeEach(() => {
  toasts.reset();
  navigate('/stacks/web');
  stackRes = { body: detail() };
  opsRes = [];
  statusRes = { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false };
  fake = installFakeFetch({
    'GET /api/stacks/web': () => stackRes,
    'GET /api/stacks/web/operations': () => ({ body: opsRes }),
    'GET /api/stacks/web/compose': { body: { content: 'services: {}\n', hash: 'h1' } },
    'GET /api/status': () => ({ body: statusRes }),
    'GET /api/stacks': { body: [] },
    'GET /api/operations?state=running': { body: [] },
    'GET /api/operations/op1': () => ({ body: { ...op(), lines: [] } }),
    'POST /api/stacks/web/actions/up': { body: op() },
    'DELETE /api/stacks/web': { body: op({ id: 'rm1', action: 'down' }) },
    'GET /api/operations/rm1': { body: { ...op({ id: 'rm1', action: 'down' }), lines: [] } },
  });
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
    const tabs = screen.getAllByRole('tab').map((t) => t.textContent);
    expect(tabs).toEqual(['Services', 'Compose', 'Logs', 'Metrics', 'Operations']);
    expect(screen.getByRole('tab', { name: 'Services' }).getAttribute('aria-selected')).toBe('true');
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
    await user().click(screen.getByRole('tab', { name: 'Services' }));
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
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Compose', 'Operations']);
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
});
