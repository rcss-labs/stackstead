import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Suspense } from 'react';
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from 'react-router';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { get } from '$lib/store';
import type { Operation, StackDetail as Detail } from '$lib/api/types';
import { changes, startLive, stopLive } from '$lib/stores/live';
import { setUnsavedChanges } from '$lib/stores/guard';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream } from '../../vitest-setup';
import { ROUTES } from '../routes';

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
let stream: FakeStream;
let stackRes: FakeResponse;
let stackGate: Promise<void> | undefined;
let postGate: Promise<void> | undefined;
let opsRes: Operation[];
let healthRes: FakeResponse;
let statusRes: { docker: string; compose: string; events: string; metricsDegraded: boolean };
let goTo: (href: string) => void;

beforeEach(() => {
  toasts.reset();
  stackRes = { body: detail() };
  stackGate = undefined;
  postGate = undefined;
  opsRes = [];
  healthRes = { body: { from: 0, to: 3600, services: [] } };
  statusRes = { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false };
  fake = installFakeFetch({
    'GET /api/stacks/web': async () => {
      if (stackGate) await stackGate;
      return stackRes;
    },
    'GET /api/stacks/db': { body: detail({ name: 'db', project: 'db', path: '/srv/stacks/db/compose.yaml' }) },
    'GET /api/stacks/web/operations': () => ({ body: opsRes }),
    'GET /api/stacks/db/operations': { body: [] },
    'GET /api/stacks/web/health?span=60m': () => healthRes,
    'GET /api/stacks/db/health?span=60m': { body: { from: 0, to: 3600, services: [] } },
    'GET /api/stacks/web/metrics': { body: { points: [] } },
    'GET /api/status': () => ({ body: statusRes }),
    'GET /api/stacks': { body: [] },
    'GET /api/operations?state=running': { body: [] },
    'POST /api/stacks/web/actions/up': async () => {
      if (postGate) await postGate;
      return { body: op() };
    },
    'POST /api/stacks/web/actions/pull': async () => {
      if (postGate) await postGate;
      return { body: op({ id: 'op2', action: 'pull' }) };
    },
    'DELETE /api/stacks/web': { body: op({ id: 'rm1', action: 'down' }) },
  });
  stream = installFakeStream({
    '/api/events': [],
    '/api/stacks/web/logs': [],
    '/api/stacks/web/metrics/live': [],
    '/api/operations/op1/stream': [],
    '/api/operations/rm1/stream': [],
  });
});
afterEach(async () => {
  cleanup();
  setUnsavedChanges(false);
  stopLive();
  stream.restore();
  fake.restore();
  await new Promise((r) => setTimeout(r, 5)); // useStore releases its subscriptions on the next task
});

function Probe() {
  const location = useLocation();
  const navigate = useNavigate();
  goTo = (href) => act(() => void navigate(href));
  return <output data-testid="location">{location.pathname + location.search}</output>;
}

/** The app's route table, with the overview swapped for a stub so leaving the page needs no overview data. */
function mount(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Suspense fallback={<p>loading page</p>}>
        <Routes>
          {ROUTES.map(({ path: pattern, screen: Screen }) => (
            <Route key={pattern} path={pattern} element={pattern === '/' ? <p>home</p> : <Screen />} />
          ))}
        </Routes>
      </Suspense>
      <Probe />
    </MemoryRouter>,
  );
}

const here = () => screen.getByTestId('location').textContent;
const calls = (method: string, path: string) => fake.calls.filter((c) => c.method === method && c.path === path).length;
const stackGets = () => calls('GET', '/api/stacks/web');
const user = () => userEvent.setup();
const startLiveFromFake = () => startLive({ fetchStatus: () => fetch('/api/status').then((r) => r.json()) });
const pushChange = (project: string, seq: number) =>
  stream.push('/api/events', `event: change\ndata: {"project":"${project}","seq":${seq}}\n\n`);

describe('stack detail screen', () => {
  it('loads the stack and shows the header, status, meta, path and one h1', async () => {
    mount('/stacks/web');
    expect(await screen.findByRole('heading', { level: 1, name: 'web' })).toBeTruthy();
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1);
    expect(screen.getByText('running')).toBeTruthy();
    expect(screen.getByText('1/1 up · managed ·', { exact: false })).toBeTruthy();
    expect(screen.getByText('/srv/stacks/web/compose.yaml')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Copy path' })).toBeTruthy();
    expect(document.title).toBe('web · Stackstead');
  });

  it('shows the not-found view with a way back on 404', async () => {
    stackRes = { status: 404, body: { error: 'stack not found' } };
    mount('/stacks/web');
    expect(await screen.findByText('Stack not found')).toBeTruthy();
    expect(screen.getByText('There is no stack named "web".')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Back to stacks' }).getAttribute('href')).toBe('/');
    expect(screen.queryByRole('tablist')).toBeNull();
    expect(stream.requests.some((r) => r.path.startsWith('/api/stacks/web/metrics/live') || r.path.startsWith('/api/stacks/web/logs'))).toBe(false);
  });

  it('first-load failure shows the banner and a busy Retry that refetches', async () => {
    stackRes = { status: 500, body: { error: 'boom' } };
    mount('/stacks/web');
    expect(await screen.findByText('Could not load the stack')).toBeTruthy();
    expect(screen.getByText('boom')).toBeTruthy();
    let release!: () => void;
    stackGate = new Promise((r) => (release = r));
    stackRes = { body: detail() };
    await user().click(screen.getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Retry' }).getAttribute('aria-busy')).toBe('true'));
    release();
    expect(await screen.findByRole('heading', { level: 1, name: 'web' })).toBeTruthy();
    expect(stackGets()).toBe(2);
  });

  it('a later failure keeps the page and shows the last-loaded warning', async () => {
    startLiveFromFake();
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await waitFor(() => expect(get(changes)?.resync).toBe(true));
    stackRes = { status: 500, body: { error: 'engine hiccup' } };
    pushChange('web', 1);
    expect(await screen.findByText('Showing the last loaded state')).toBeTruthy();
    expect(screen.getByText('engine hiccup')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 1, name: 'web' })).toBeTruthy();
  });

  it('a parse error shows the message and the guidance', async () => {
    stackRes = { body: detail({ status: 'unavailable', parseError: 'yaml: line 3: mapping values are not allowed', services: [] }) };
    mount('/stacks/web');
    const banner = await screen.findByRole('alert');
    expect(within(banner).getByText('Compose file cannot be parsed')).toBeTruthy();
    expect(banner.querySelector('pre')?.textContent).toBe('yaml: line 3: mapping values are not allowed');
    expect(within(banner).getByText(/Fix it in the Compose file/)).toBeTruthy();
  });

  it('a missing compose file names it and offers no editor', async () => {
    stackRes = { body: detail({ status: 'unavailable', parseError: 'open /srv/stacks/web/compose.yaml: no such file or directory', services: [] }) };
    mount('/stacks/web');
    const banner = await screen.findByRole('alert');
    expect(within(banner).getByText('Compose file is missing')).toBeTruthy();
    expect(banner.querySelector('pre')?.textContent).toMatch(/no such file/);
    expect(within(banner).getByText('Restore the file on disk to edit this stack.')).toBeTruthy();
    expect(screen.queryByRole('textbox')).toBeNull();
  });

  it('Up tracks the operation and opens the operations item', async () => {
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await user().click(screen.getByRole('button', { name: 'Up' }));
    await waitFor(() => expect(here()).toBe('/stacks/web?tab=operations&op=op1'));
    const pending = get(toasts).filter((t) => t.pending);
    expect(pending.map((t) => t.message)).toEqual(['up · web']);
  });

  it('a 409 on an action toasts busy and a 503 refreshes status', async () => {
    fake.restore();
    fake = installFakeFetch({
      'GET /api/stacks/web': { body: detail() },
      'GET /api/stacks/web/operations': { body: [] },
      'GET /api/stacks/web/health?span=60m': { body: { from: 0, to: 3600, services: [] } },
      'POST /api/stacks/web/actions/up': { status: 409, body: { error: 'operation already running' } },
      'POST /api/stacks/web/actions/pull': { status: 503, body: { error: 'docker unavailable' } },
      'GET /api/status': { body: statusRes },
    });
    startLiveFromFake();
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await user().click(screen.getByRole('button', { name: 'Up' }));
    await waitFor(() => expect(get(toasts).some((t) => t.kind === 'warning' && /already running/.test(t.message))).toBe(true));
    const before = calls('GET', '/api/status');
    await user().click(screen.getByRole('button', { name: 'Pull' }));
    await waitFor(() => expect(calls('GET', '/api/status')).toBeGreaterThan(before));
    expect(get(toasts).some((t) => t.kind === 'danger' && /docker unavailable/.test(t.message))).toBe(true);
  });

  it('lifecycle buttons are soft-disabled with a reason while an operation runs', async () => {
    opsRes = [op()];
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Up' }).getAttribute('aria-disabled')).toBe('true'));
    expect(screen.getByRole('button', { name: 'Up' }).getAttribute('title')).toBe('an operation is already running');
    expect(screen.getByRole('button', { name: 'Remove' }).getAttribute('aria-disabled')).toBe('true');
  });

  it('lifecycle buttons are soft-disabled with a reason when compose is unavailable', async () => {
    statusRes = { ...statusRes, compose: 'unavailable' };
    startLiveFromFake();
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Up' }).getAttribute('aria-disabled')).toBe('true'));
    expect(screen.getByRole('button', { name: 'Up' }).getAttribute('title')).toBe('docker compose plugin not found');
    expect(screen.getByRole('button', { name: 'Remove' }).getAttribute('aria-disabled')).toBeNull();
  });

  it('lifecycle buttons name the parse error as the reason', async () => {
    stackRes = { body: detail({ status: 'unavailable', parseError: 'bad yaml', services: [] }) };
    mount('/stacks/web');
    await screen.findByRole('alert');
    expect(screen.getByRole('button', { name: 'Up' }).getAttribute('title')).toBe('the compose file cannot be parsed');
  });

  it('double click on Pull sends one request', async () => {
    let release!: () => void;
    postGate = new Promise((r) => (release = r));
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await user().dblClick(screen.getByRole('button', { name: 'Pull' }));
    release();
    await waitFor(() => expect(here()).toContain('op=op2'));
    expect(calls('POST', '/api/stacks/web/actions/pull')).toBe(1);
  });

  it('removing a stack posts the removal and opens its operation', async () => {
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await user().click(screen.getByRole('button', { name: 'Remove' }));
    const dialog = await screen.findByRole('alertdialog');
    await user().click(within(dialog).getByRole('checkbox', { name: /also remove volumes/i }));
    await user().click(within(dialog).getByRole('button', { name: 'Remove' }));
    await waitFor(() => expect(here()).toBe('/stacks/web?tab=operations&op=rm1'));
    expect(calls('DELETE', '/api/stacks/web?volumes=true')).toBe(1);
  });

  it('a 404 after a remove operation was started counts as a successful removal', async () => {
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await user().click(screen.getByRole('button', { name: 'Remove' }));
    await user().click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Remove' }));
    await waitFor(() => expect(here()).toBe('/stacks/web?tab=operations&op=rm1'));
    stackRes = { status: 404, body: { error: 'stack not found' } };
    startLiveFromFake();
    await waitFor(() => expect(here()).toBe('/'));
    expect(get(toasts).filter((t) => t.kind === 'success')).toHaveLength(1);
  });

  it('navigating from one stack to another restarts the page', async () => {
    mount('/stacks/web?tab=operations&op=op1');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await waitFor(() => expect(calls('GET', '/api/stacks/web/health?span=60m')).toBe(1));
    goTo('/stacks/db');
    expect(await screen.findByRole('heading', { level: 1, name: 'db' })).toBeTruthy();
    await waitFor(() => expect(calls('GET', '/api/stacks/db')).toBe(1));
    await waitFor(() => expect(calls('GET', '/api/stacks/db/health?span=60m')).toBe(1));
    expect(calls('GET', '/api/stacks/web')).toBe(1);
    expect(document.title).toBe('db · Stackstead');
    expect(here()).toBe('/stacks/db');
  });

  it('refetches when a change for its project arrives or on resync, not for another project', async () => {
    startLiveFromFake();
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await waitFor(() => expect(get(changes)?.resync).toBe(true));
    await waitFor(() => expect(stackGets()).toBe(2)); // initial + resync on open
    pushChange('other', 1);
    await new Promise((r) => setTimeout(r, 50));
    expect(stackGets()).toBe(2);
    pushChange('web', 2);
    await waitFor(() => expect(stackGets()).toBe(3));
  });

  it('collapses a burst of changes into at most two refetches of each endpoint', async () => {
    startLiveFromFake();
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await waitFor(() => expect(stackGets()).toBe(2)); // initial + resync on open
    const endpoints = ['/api/stacks/web', '/api/stacks/web/operations', '/api/stacks/web/health?span=60m'];
    const before = endpoints.map((p) => calls('GET', p));
    for (let i = 1; i <= 10; i++) pushChange('web', i);
    await new Promise((r) => setTimeout(r, 1500));
    endpoints.forEach((p, i) => {
      const extra = calls('GET', p) - before[i]!;
      expect(extra, p).toBeGreaterThanOrEqual(1);
      expect(extra, p).toBeLessThanOrEqual(2);
    });
  });

  describe('health timeline', () => {
    const twoServices = () => {
      const base = detail();
      return detail({ serviceCount: 2, services: [base.services[0]!, { ...base.services[0]!, name: 'db' }] });
    };
    const region = () => screen.findByRole('region', { name: 'Health · last 60 min' });

    it('fetches health once on load, refetches on this project\'s change and extends to now', async () => {
      const now = Math.floor(Date.now() / 1000);
      healthRes = {
        body: {
          from: now - 3600,
          to: now - 120,
          services: [
            {
              service: 'app',
              restarts: 0,
              runs: [
                { state: 'healthy', from: now - 3600, to: now - 600 },
                { state: 'unhealthy', from: now - 600, to: now - 120 },
              ],
            },
          ],
        },
      };
      startLiveFromFake();
      mount('/stacks/web');
      const lane = within(await region());
      const spans = await waitFor(() => {
        const found = Array.from(lane.getByRole('img', { name: /^app: healthy .*unhealthy/ }).children) as HTMLElement[];
        expect(found).toHaveLength(2);
        return found;
      });
      const flex = parseFloat(spans[1]!.style.flex);
      expect(flex - 480).toBeGreaterThanOrEqual(120);
      expect(flex - 480).toBeLessThanOrEqual(122);
      await waitFor(() => expect(get(changes)?.resync).toBe(true));
      await waitFor(() => expect(stackGets()).toBe(2));
      const before = calls('GET', '/api/stacks/web/health?span=60m');
      expect(before).toBeLessThanOrEqual(2); // initial, plus at most the resync on open
      pushChange('web', 5);
      await waitFor(() => expect(calls('GET', '/api/stacks/web/health?span=60m')).toBe(before + 1));
    });

    it('a lane name navigates to that service\'s overview', async () => {
      stackRes = { body: twoServices() };
      mount('/stacks/web');
      const lane = within(await region());
      await user().click(lane.getByRole('button', { name: 'db' }));
      await waitFor(() => expect(here()).toBe('/stacks/web?service=db'));
      expect(lane.getByRole('button', { name: 'db' }).getAttribute('aria-pressed')).toBe('true');
    });

    it('renders nodata lanes while health has failed, with no error banner', async () => {
      healthRes = { status: 500, body: { error: 'boom' } };
      stackRes = { body: twoServices() };
      mount('/stacks/web');
      const lane = within(await region());
      await waitFor(() => expect(calls('GET', '/api/stacks/web/health?span=60m')).toBe(1));
      for (const service of ['app', 'db']) {
        const spans = Array.from(lane.getByRole('img', { name: new RegExp(`^${service}: `) }).children) as HTMLElement[];
        expect(spans.map((s) => s.dataset.tone)).toEqual(['nodata']);
      }
      expect(screen.queryByText('Showing the last loaded state')).toBeNull();
      expect(screen.queryByText('Could not load the stack')).toBeNull();
    });
  });
});
