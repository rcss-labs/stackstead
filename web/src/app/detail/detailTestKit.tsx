// Shared fixtures and the fake backend for the detail screen tests (rail, service pane).
import { act, render } from '@testing-library/react';
import { Suspense } from 'react';
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from 'react-router';
import type { Operation, ServiceDetail, StackDetail } from '$lib/api/types';
import { stopLive } from '$lib/stores/live';
import { setUnsavedChanges } from '$lib/stores/guard';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream } from '../../vitest-setup';
import { ROUTES } from '../routes';

export const appService = (over: Partial<ServiceDetail> = {}): ServiceDetail => ({
  name: 'app',
  config: { image: 'nginx:1', ports: ['80:80'], limits: {} },
  readOnly: [],
  containers: [{ id: 'abc', name: 'web-app-1', service: 'app', state: 'running', status: 'Up 2 minutes', health: 'none' }],
  ...over,
});

export const dbService = (over: Partial<ServiceDetail> = {}): ServiceDetail => ({
  name: 'db',
  config: { image: 'postgres:16', limits: {} },
  readOnly: [],
  containers: [{ id: 'def', name: 'web-db-1', service: 'db', state: 'running', status: 'Up 2 minutes', health: 'healthy' }],
  ...over,
});

export const stackDetail = (over: Partial<StackDetail> = {}): StackDetail => ({
  name: 'web',
  path: '/srv/stacks/web/compose.yaml',
  project: 'web',
  origin: 'managed',
  status: 'running',
  serviceCount: 2,
  running: 2,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
  hash: 'h1',
  services: [appService(), dbService()],
  ...over,
});

export const operation = (over: Partial<Operation> = {}): Operation => ({
  id: 'op1',
  stack: 'web',
  action: 'up',
  state: 'running',
  startedAt: '2026-10-14T10:00:00Z',
  ...over,
});

export interface Harness {
  fake: FakeFetch;
  stream: FakeStream;
  /** Mutable: what GET /api/stacks/web answers. */
  stackRes: FakeResponse;
  stackGate: Promise<void> | undefined;
  operations: Operation[];
  composeRes: FakeResponse;
  goTo: (href: string) => void;
  calls: (method: string, path: string) => number;
  liveRequests: () => FakeStream['requests'];
  /** Calls whose path starts with `prefix`. */
  callsStarting: (method: string, prefix: string) => number;
}

export function installHarness(): Harness {
  toasts.reset();
  const h = {} as Harness;
  h.stackRes = { body: stackDetail() };
  h.stackGate = undefined;
  h.operations = [operation({ id: 'op1' }), operation({ id: 'op2', action: 'pull', state: 'succeeded' })];
  h.composeRes = { body: { content: 'services: {}\n', hash: 'c1' } };
  h.fake = installFakeFetch({
    'GET /api/stacks/web': async () => {
      if (h.stackGate) await h.stackGate;
      return h.stackRes;
    },
    'GET /api/stacks/db': { body: stackDetail({ name: 'db', project: 'db', path: '/srv/stacks/db/compose.yaml' }) },
    'GET /api/stacks/web/operations': () => ({ body: h.operations }),
    'GET /api/stacks/db/operations': { body: [] },
    'GET /api/stacks/web/health?span=60m': { body: { from: 0, to: 3600, services: [] } },
    'GET /api/stacks/db/health?span=60m': { body: { from: 0, to: 3600, services: [] } },
    'GET /api/stacks/web/metrics': { body: { points: [] } },
    'GET /api/stacks/web/compose': () => h.composeRes,
    'GET /api/status': { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
    'GET /api/stacks': { body: [] },
    'GET /api/operations?state=running': { body: [] },
  });
  h.stream = installFakeStream({
    '/api/events': [],
    '/api/stacks/web/logs': [],
    '/api/stacks/web/metrics/live': [],
    '/api/stacks/db/metrics/live': [],
    '/api/operations/op1/stream': [],
  });
  h.calls = (method, path) => h.fake.calls.filter((c) => c.method === method && c.path === path).length;
  h.callsStarting = (method, prefix) => h.fake.calls.filter((c) => c.method === method && c.path.startsWith(prefix)).length;
  h.liveRequests = () => h.stream.requests.filter((r) => r.path.startsWith('/api/stacks/web/metrics/live'));
  h.goTo = () => {};
  return h;
}

export async function removeHarness(h: Harness): Promise<void> {
  setUnsavedChanges(false);
  stopLive();
  h.stream.restore();
  h.fake.restore();
  await new Promise((r) => setTimeout(r, 5)); // useStore releases its subscriptions on the next task
}

/** The app's route table with the overview stubbed; `h.goTo` navigates the mounted router. */
export function mountDetail(h: Harness, path: string) {
  function Probe() {
    const location = useLocation();
    const navigate = useNavigate();
    h.goTo = (href) => act(() => void navigate(href));
    return <output data-testid="location">{location.pathname + location.search}</output>;
  }
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
