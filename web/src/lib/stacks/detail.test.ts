import { get, writable } from '$lib/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from '../api/client';
import type { HealthHistory, MetricsHistory, Operation, RawCompose, StackDetail } from '../api/types';
import type { LiveChange } from '../stores/live';
import { rangeToQuery } from '../metrics/series';
import { createStackDetail, LAST_SERVICE_REASON, removeServiceReason, type StackDetailDeps } from './detail';

const detail = (over: Partial<StackDetail> = {}): StackDetail => ({
  name: 'web',
  path: '/srv/stacks/web/compose.yaml',
  project: 'web',
  origin: 'managed',
  status: 'running',
  serviceCount: 1,
  running: 1,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
  libraryUpdates: 0,
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
const withServices = (...names: string[]): StackDetail => {
  const base = detail();
  return detail({ serviceCount: names.length, services: names.map((name) => ({ ...base.services[0], name })) });
};
const history = (to = 3600): HealthHistory => ({ from: to - 3600, to, services: [] });

const NOW = new Date('2026-10-19T12:00:00Z');

/** A promise that settles only when the test says so. */
function gate<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const settle = () => vi.advanceTimersByTimeAsync(0);

function setup(over: Partial<StackDetailDeps> = {}) {
  const changes = writable<LiveChange | null>(null);
  const operationsRefresh = writable(0);
  const base = {
    stack: vi.fn<StackDetailDeps['stack']>(async () => detail()),
    operations: vi.fn<StackDetailDeps['operations']>(async () => [op()]),
    health: vi.fn<StackDetailDeps['health']>(async () => history()),
    compose: vi.fn<StackDetailDeps['compose']>(async (): Promise<RawCompose> => ({ content: 'services: {}\n', hash: 'h1' })),
    metricsHistory: vi.fn<StackDetailDeps['metricsHistory']>(async (): Promise<MetricsHistory> => ({ points: [] })),
    act: vi.fn<StackDetailDeps['act']>(async () => op()),
    remove: vi.fn<StackDetailDeps['remove']>(async () => op({ id: 'rm1', action: 'remove' })),
    cancel: vi.fn<StackDetailDeps['cancel']>(async () => op({ state: 'cancelled' })),
    removeService: vi.fn<StackDetailDeps['removeService']>(async () => detail({ hash: 'h2' })),
    changes,
    operationsRefresh,
    refreshStacks: vi.fn(async () => {}),
    refreshRunningOps: vi.fn(),
    refreshStatus: vi.fn(async () => {}),
    track: vi.fn<StackDetailDeps['track']>(),
    navigate: vi.fn<StackDetailDeps['navigate']>(),
    toasts: { push: vi.fn<StackDetailDeps['toasts']['push']>(() => 1) },
    now: () => NOW,
  };
  // Overrides are always vi.fn mocks, so the merged object keeps the mock-typed shape.
  const deps = { ...base, ...over } as typeof base;
  const ctl = createStackDetail('web', deps);
  return { ctl, deps, changes, operationsRefresh };
}

let stopCtl: (() => void) | undefined;
async function started(over: Partial<StackDetailDeps> = {}) {
  const s = setup(over);
  s.ctl.start();
  stopCtl = () => s.ctl.stop();
  await settle();
  return s;
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});
afterEach(() => {
  stopCtl?.();
  stopCtl = undefined;
  vi.useRealTimers();
});

describe('loading and refetching', () => {
  it('start loads the stack, its operations and its health once and exposes them', async () => {
    const { ctl, deps } = await started();
    expect(deps.stack).toHaveBeenCalledTimes(1);
    expect(deps.stack).toHaveBeenCalledWith('web');
    expect(deps.operations).toHaveBeenCalledTimes(1);
    expect(deps.health).toHaveBeenCalledTimes(1);
    expect(deps.health).toHaveBeenCalledWith('web', { span: '60m' }, expect.any(AbortSignal));
    expect(deps.compose).not.toHaveBeenCalled();
    expect(get(ctl.detail)?.hash).toBe('h1');
    expect(get(ctl.operations)).toEqual([op()]);
    expect(get(ctl.health)).toEqual(history());
    expect(get(ctl.notFound)).toBe(false);
    expect(get(ctl.loadError)).toBe('');
  });

  it('refetches on a change for its project or a resync, not for another project', async () => {
    const { deps, changes } = await started();
    changes.set({ project: 'other', seq: 1 });
    await vi.advanceTimersByTimeAsync(1500);
    expect(deps.stack).toHaveBeenCalledTimes(1);
    expect(deps.health).toHaveBeenCalledTimes(1);

    changes.set({ project: 'web', seq: 2 });
    await vi.advanceTimersByTimeAsync(1500);
    expect(deps.stack).toHaveBeenCalledTimes(2);
    expect(deps.operations).toHaveBeenCalledTimes(2);
    expect(deps.health).toHaveBeenCalledTimes(2);

    changes.set({ project: '', seq: 3, resync: true });
    await settle(); // a resync flushes at once, without waiting for the debounce
    expect(deps.stack).toHaveBeenCalledTimes(3);
    expect(deps.health).toHaveBeenCalledTimes(3);
  });

  it('collapses a burst of ten changes into at most two refetches of each endpoint', async () => {
    const { deps, changes } = await started();
    for (let i = 1; i <= 10; i++) {
      changes.set({ project: 'web', seq: i });
      await vi.advanceTimersByTimeAsync(50);
    }
    await vi.advanceTimersByTimeAsync(1500);
    for (const endpoint of [deps.stack, deps.operations, deps.health]) {
      const refetches = endpoint.mock.calls.length - 1;
      expect(refetches).toBeGreaterThanOrEqual(1);
      expect(refetches).toBeLessThanOrEqual(2);
    }
  });

  it('an operationsRefresh tick reloads operations only', async () => {
    const { deps, operationsRefresh } = await started();
    operationsRefresh.set(1);
    await settle();
    expect(deps.operations).toHaveBeenCalledTimes(2);
    expect(deps.stack).toHaveBeenCalledTimes(1);
    expect(deps.health).toHaveBeenCalledTimes(1);
  });

  it('a late stack response is dropped by sequence', async () => {
    const first = gate<StackDetail>();
    const { ctl, deps } = await started({
      stack: vi
        .fn<StackDetailDeps['stack']>()
        .mockReturnValueOnce(first.promise)
        .mockResolvedValueOnce(detail({ hash: 'newer' })),
    });
    await ctl.load();
    expect(get(ctl.detail)?.hash).toBe('newer');
    first.resolve(detail({ hash: 'older' }));
    await settle();
    expect(get(ctl.detail)?.hash).toBe('newer');
    expect(deps.stack).toHaveBeenCalledTimes(2);
  });

  it('404 sets notFound; other errors set loadError and keep the last detail', async () => {
    const missing = await started({ stack: vi.fn(async () => Promise.reject(new ApiError(404, 'stack not found'))) });
    expect(get(missing.ctl.notFound)).toBe(true);
    expect(get(missing.ctl.loadError)).toBe('');
    missing.ctl.stop();

    const { ctl, deps } = await started();
    deps.stack.mockRejectedValueOnce(new Error('boom'));
    await ctl.load();
    expect(get(ctl.loadError)).toBe('boom');
    expect(get(ctl.notFound)).toBe(false);
    expect(get(ctl.detail)?.hash).toBe('h1');
    await ctl.load(); // a later success clears the error
    expect(get(ctl.loadError)).toBe('');
  });

  it('a failed health fetch keeps the last history', async () => {
    const { ctl, deps } = await started();
    deps.health.mockRejectedValueOnce(new Error('unavailable'));
    await ctl.loadHealth();
    expect(get(ctl.health)).toEqual(history());
  });

  it('compose loads lazily once and refetches with the next change', async () => {
    const { ctl, deps, changes } = await started();
    expect(get(ctl.compose)).toBeUndefined();
    await ctl.loadCompose();
    await ctl.loadCompose();
    expect(deps.compose).toHaveBeenCalledTimes(1);
    expect(get(ctl.compose)?.hash).toBe('h1');

    deps.compose.mockResolvedValueOnce({ content: 'services: {a: {}}\n', hash: 'h2' });
    changes.set({ project: 'web', seq: 1 });
    await vi.advanceTimersByTimeAsync(1500);
    expect(deps.compose).toHaveBeenCalledTimes(2);
    expect(get(ctl.compose)?.hash).toBe('h2');

    deps.compose.mockRejectedValueOnce(new Error('read failed'));
    changes.set({ project: 'web', seq: 2 });
    await vi.advanceTimersByTimeAsync(1500);
    expect(get(ctl.composeError)).toBe('read failed');
  });

  it('loadCpuHour aligns its window to the server 120 s grid so the first bucket is kept', async () => {
    const offGrid = new Date('2026-10-19T12:01:10Z');
    const metricsHistory = vi.fn<StackDetailDeps['metricsHistory']>(async () => ({
      points: [{ time: '2026-10-19T11:00:00Z', service: 'app', cpu: 7, mem: 0, memLimit: 0 }],
    }));
    const { ctl } = await started({ metricsHistory, now: () => offGrid });
    await ctl.loadCpuHour('app');
    expect(metricsHistory).toHaveBeenCalledWith(
      'web',
      { from: '2026-10-19T11:00:00Z', to: '2026-10-19T12:00:00Z', step: '2m', service: 'app' },
      expect.any(AbortSignal),
    );
    const values = get(ctl.cpuHour).get('app');
    expect(values).toHaveLength(30);
    expect(values?.[0]).toBe(7);
  });

  it('loadCpuHour: a response that lands after a newer request for the service is ignored', async () => {
    const a = gate<MetricsHistory>();
    const b = gate<MetricsHistory>();
    const metricsHistory = vi.fn<StackDetailDeps['metricsHistory']>().mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);
    const { ctl, changes } = await started({ metricsHistory });
    const first = ctl.loadCpuHour('app');
    changes.set({ project: 'web', seq: 1 });
    await vi.advanceTimersByTimeAsync(1500);
    const second = ctl.loadCpuHour('app');
    const point = (cpu: number) => ({ points: [{ time: '2026-10-19T11:00:00Z', service: 'app', cpu, mem: 0, memLimit: 0 }] });
    b.resolve(point(2));
    await second;
    a.resolve(point(1));
    await first;
    expect(get(ctl.cpuHour).get('app')?.[0]).toBe(2);
  });

  it('loadCpuHour: a failed older request does not unmark the newer in-flight one', async () => {
    const a = gate<MetricsHistory>();
    const b = gate<MetricsHistory>();
    const metricsHistory = vi.fn<StackDetailDeps['metricsHistory']>().mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise);
    const { ctl, changes } = await started({ metricsHistory });
    const first = ctl.loadCpuHour('app');
    changes.set({ project: 'web', seq: 1 });
    await vi.advanceTimersByTimeAsync(1500);
    void ctl.loadCpuHour('app');
    a.reject(new Error('aborted'));
    await first;
    await ctl.loadCpuHour('app');
    expect(metricsHistory).toHaveBeenCalledTimes(2);
  });

  it('loadCpuHour caches per service and clears on refetch', async () => {
    const { from, to } = rangeToQuery('1h', NOW);
    const at = (minutes: number) => new Date(Date.parse(from) + minutes * 60_000).toISOString();
    const metricsHistory = vi.fn<StackDetailDeps['metricsHistory']>(async () => ({
      points: [
        { time: at(0), service: 'app', cpu: 10, mem: 0, memLimit: 0 },
        { time: at(4), service: 'app', cpu: 30, mem: 0, memLimit: 0 },
      ],
    }));
    const { ctl, changes } = await started({ metricsHistory });
    await ctl.loadCpuHour('app');
    await ctl.loadCpuHour('app');
    expect(metricsHistory).toHaveBeenCalledTimes(1);
    expect(metricsHistory).toHaveBeenCalledWith('web', { from, to, step: '2m', service: 'app' }, expect.any(AbortSignal));
    const values = get(ctl.cpuHour).get('app');
    expect(values).toHaveLength(30);
    expect(values?.slice(0, 4)).toEqual([10, null, 30, null]);

    changes.set({ project: 'web', seq: 1 });
    await vi.advanceTimersByTimeAsync(1500);
    await ctl.loadCpuHour('app');
    expect(metricsHistory).toHaveBeenCalledTimes(2);
  });
});

describe('actions and operations', () => {
  it.each([
    ['success', undefined],
    ['409', new ApiError(409, 'already running')],
    ['503', new ApiError(503, 'docker unavailable')],
  ] as const)('act tracks the operation, navigates to it and refreshes running ops; a 409 toasts a warning, a 503 refreshes status (%s)', async (kind, error) => {
    const { ctl, deps } = await started();
    if (error) deps.act.mockRejectedValueOnce(error);
    await ctl.act('up');
    expect(deps.act).toHaveBeenCalledWith('web', 'up');
    if (kind === 'success') {
      expect(deps.track).toHaveBeenCalledWith({ id: 'op1', stack: 'web', action: 'up' });
      expect(deps.navigate).toHaveBeenCalledWith('/stacks/web?tab=operations&op=op1');
      expect(deps.refreshRunningOps).toHaveBeenCalledTimes(1);
      expect(deps.track.mock.invocationCallOrder[0]).toBeLessThan(deps.refreshRunningOps.mock.invocationCallOrder[0]);
      expect(deps.toasts.push).not.toHaveBeenCalled();
    } else {
      expect(deps.track).not.toHaveBeenCalled();
      expect(deps.navigate).not.toHaveBeenCalled();
      expect(deps.toasts.push).toHaveBeenCalledWith({
        kind: kind === '409' ? 'warning' : 'danger',
        message: `Could not run up: ${error?.message}`,
      });
      expect(deps.refreshStatus).toHaveBeenCalledTimes(kind === '503' ? 1 : 0);
    }
    expect(get(ctl.acting)).toBe(false);
  });

  it('act ignores a second call while one is in flight', async () => {
    const pending = gate<Operation>();
    const { ctl, deps } = await started({ act: vi.fn(() => pending.promise) });
    const first = ctl.act('up');
    expect(get(ctl.acting)).toBe(true);
    await ctl.act('down');
    expect(deps.act).toHaveBeenCalledTimes(1);
    pending.resolve(op());
    await first;
    expect(get(ctl.acting)).toBe(false);
  });

  it('removeStack does not track (the controller alone reports it) and, on a succeeded done event, toasts Removed, refreshes and navigates home once', async () => {
    const { ctl, deps } = await started();
    await ctl.removeStack(true);
    expect(deps.remove).toHaveBeenCalledWith('web', true);
    expect(deps.track).not.toHaveBeenCalled();
    expect(deps.navigate).toHaveBeenCalledWith('/stacks/web?tab=operations&op=rm1');
    expect(deps.refreshRunningOps).toHaveBeenCalledTimes(1);

    ctl.operationFinished(op({ id: 'rm1', action: 'remove', state: 'succeeded' }));
    ctl.operationFinished(op({ id: 'rm1', action: 'remove', state: 'succeeded' }));
    expect(deps.toasts.push).toHaveBeenCalledTimes(1);
    expect(deps.toasts.push).toHaveBeenCalledWith({ kind: 'success', message: 'Removed web' });
    expect(deps.navigate.mock.calls.filter(([href]) => href === '/')).toHaveLength(1);
    expect(deps.refreshStacks).toHaveBeenCalled();
    expect(deps.stack).toHaveBeenCalledTimes(1); // the removal's own done event never reloads the stack
  });

  it('removeStack reports a failure with its prefix', async () => {
    const { ctl, deps } = await started();
    deps.remove.mockRejectedValueOnce(new ApiError(409, 'busy'));
    await ctl.removeStack(false);
    expect(deps.toasts.push).toHaveBeenCalledWith({ kind: 'warning', message: 'Could not remove the stack: busy' });
    expect(deps.track).not.toHaveBeenCalled();
  });

  it('a failed removal clears the pending removal and toasts danger with the state spaced', async () => {
    const { ctl, deps } = await started();
    await ctl.removeStack(false);
    ctl.operationFinished(op({ id: 'rm1', action: 'remove', state: 'timed_out' }));
    expect(deps.toasts.push).toHaveBeenCalledWith({ kind: 'danger', message: 'Removing web timed out' });
    expect(deps.navigate).not.toHaveBeenCalledWith('/');
    // With no removal pending, a 404 is a plain "not found" again.
    deps.stack.mockRejectedValueOnce(new ApiError(404, 'stack not found'));
    await ctl.load();
    expect(get(ctl.notFound)).toBe(true);
    expect(deps.navigate).not.toHaveBeenCalledWith('/');
  });

  it('a 404 after a remove started counts as a successful removal', async () => {
    const { ctl, deps } = await started();
    await ctl.removeStack(false);
    deps.stack.mockRejectedValue(new ApiError(404, 'stack not found'));
    await ctl.load();
    ctl.operationFinished(op({ id: 'rm1', action: 'remove', state: 'succeeded' }));
    expect(get(ctl.notFound)).toBe(false);
    expect(deps.navigate).toHaveBeenCalledWith('/');
    expect(deps.toasts.push.mock.calls.filter(([t]) => t.kind === 'success')).toHaveLength(1);
  });

  it('operationFinished for another operation reloads the stack and operations', async () => {
    const { ctl, deps } = await started();
    ctl.operationFinished(op({ state: 'succeeded' }));
    await settle();
    expect(deps.refreshRunningOps).toHaveBeenCalledTimes(1);
    expect(deps.refreshStacks).toHaveBeenCalledTimes(1);
    expect(deps.stack).toHaveBeenCalledTimes(2);
    expect(deps.operations).toHaveBeenCalledTimes(2);
    expect(deps.toasts.push).not.toHaveBeenCalled();
  });

  it('cancelOperation toasts on failure', async () => {
    const { ctl, deps } = await started();
    await ctl.cancelOperation('op1');
    expect(deps.cancel).toHaveBeenCalledWith('op1');
    expect(deps.toasts.push).not.toHaveBeenCalled();
    deps.cancel.mockRejectedValueOnce(new Error('nope'));
    await ctl.cancelOperation('op1');
    expect(deps.toasts.push).toHaveBeenCalledWith({ kind: 'danger', message: 'Could not cancel: nope' });
  });

  it('reloadDetail replaces the detail and acceptDetail outranks a pending load', async () => {
    const { ctl, deps } = await started();
    deps.stack.mockResolvedValueOnce(detail({ hash: 'h5' }));
    await expect(ctl.reloadDetail()).resolves.toMatchObject({ hash: 'h5' });
    expect(get(ctl.detail)?.hash).toBe('h5');

    const pending = gate<StackDetail>();
    deps.stack.mockReturnValueOnce(pending.promise);
    const load = ctl.load();
    ctl.acceptDetail(detail({ hash: 'h6' }));
    pending.resolve(detail({ hash: 'stale' }));
    await load;
    expect(get(ctl.detail)?.hash).toBe('h6');
  });
});

describe('remove service', () => {
  it('the last service cannot be removed', async () => {
    expect(removeServiceReason(detail())).toBe(LAST_SERVICE_REASON);
    expect(LAST_SERVICE_REASON).toBe('The last service cannot be removed; delete the stack instead.');
    expect(removeServiceReason(withServices('app', 'cache'))).toBeUndefined();

    const { ctl, deps } = await started();
    ctl.askRemoveService('app');
    expect(get(ctl.removing)).toEqual({ service: 'app', hash: 'h1', pending: false, disabledReason: LAST_SERVICE_REASON });
    await ctl.confirmRemoveService();
    expect(deps.removeService).not.toHaveBeenCalled();
  });

  it('a 409 changed on disk reloads, takes the new hash and asks again', async () => {
    const { ctl, deps } = await started({ stack: vi.fn(async () => withServices('app', 'cache')) });
    deps.removeService.mockImplementationOnce(async () => {
      deps.stack.mockResolvedValue({ ...withServices('app', 'cache'), hash: 'h2' });
      throw new ApiError(409, 'compose changed on disk');
    });
    deps.removeService.mockResolvedValueOnce(detail({ hash: 'h3' }));
    ctl.askRemoveService('cache');
    await ctl.confirmRemoveService();
    expect(get(ctl.removing)).toEqual({
      service: 'cache',
      hash: 'h2',
      pending: false,
      error: 'The compose file changed and was reloaded. Confirm again to remove.',
    });
    expect(get(ctl.detail)?.hash).toBe('h2');
    expect(deps.removeService).toHaveBeenCalledTimes(1);

    await ctl.confirmRemoveService();
    expect(deps.removeService.mock.calls).toEqual([
      ['web', 'cache', 'h1'],
      ['web', 'cache', 'h2'],
    ]);
    expect(get(ctl.removing)).toBeUndefined();
    expect(get(ctl.detail)?.hash).toBe('h3');
    expect(deps.toasts.push).toHaveBeenCalledWith({ kind: 'success', message: 'Removed cache from compose.yaml. Run Up to apply.' });
    expect(deps.refreshStacks).toHaveBeenCalled();
  });

  it('keeps a dependency refusal visible and disables confirm', async () => {
    const { ctl, deps } = await started({ stack: vi.fn(async () => withServices('app', 'cache')) });
    deps.removeService.mockRejectedValueOnce(new ApiError(409, 'app depends_on cache'));
    ctl.askRemoveService('cache');
    await ctl.confirmRemoveService();
    expect(get(ctl.removing)).toMatchObject({ error: 'app depends_on cache', disabledReason: 'app depends_on cache', pending: false });
    await ctl.confirmRemoveService();
    expect(deps.removeService).toHaveBeenCalledTimes(1);
  });

  it('double confirm sends one DELETE and marks pending', async () => {
    const pending = gate<StackDetail>();
    const { ctl, deps } = await started({
      stack: vi.fn(async () => withServices('app', 'cache')),
      removeService: vi.fn(() => pending.promise),
    });
    ctl.askRemoveService('cache');
    const first = ctl.confirmRemoveService();
    await ctl.confirmRemoveService();
    expect(deps.removeService).toHaveBeenCalledTimes(1);
    expect(get(ctl.removing)?.pending).toBe(true);
    pending.resolve(detail({ hash: 'h2' }));
    await first;
    expect(get(ctl.removing)).toBeUndefined();
  });

  it("a cancelled session's late response refetches instead of replacing the newer session", async () => {
    const pending = gate<StackDetail>();
    const { ctl, deps } = await started({
      stack: vi.fn(async () => withServices('app', 'cache', 'worker')),
      removeService: vi.fn(() => pending.promise),
    });
    ctl.askRemoveService('cache');
    const confirm = ctl.confirmRemoveService();
    ctl.cancelRemoveService();
    expect(get(ctl.removing)).toBeUndefined();
    ctl.askRemoveService('worker');
    const before = deps.stack.mock.calls.length;
    pending.resolve(detail({ hash: 'h2' }));
    await confirm;
    await settle();
    expect(deps.stack.mock.calls.length).toBeGreaterThan(before);
    expect(get(ctl.removing)).toEqual({ service: 'worker', hash: 'h1', pending: false });
    expect(deps.toasts.push).not.toHaveBeenCalled();
  });

  it.each(['last', 'missing', 'failed'] as const)('stale remove rechecks %s reload outcome', async (outcome) => {
    const { ctl, deps } = await started({ stack: vi.fn(async () => withServices('app', 'cache')) });
    deps.removeService.mockImplementationOnce(async () => {
      if (outcome === 'failed') deps.stack.mockRejectedValue(new ApiError(500, 'read failed'));
      else deps.stack.mockResolvedValue({ ...withServices(...(outcome === 'last' ? ['cache'] : ['app'])), hash: 'h2' });
      throw new ApiError(409, 'compose changed on disk');
    });
    ctl.askRemoveService('cache');
    await ctl.confirmRemoveService();
    const session = get(ctl.removing);
    expect(session?.error).toMatch(outcome === 'last' ? /last service/ : outcome === 'missing' ? /no longer exists/ : /Could not reload.*read failed/);
    expect(session?.disabledReason).toBe(outcome === 'failed' ? undefined : session?.error);
    expect(session?.pending).toBe(false);
  });

  it('a pending detail read cannot overwrite an accepted remove result', async () => {
    const { ctl, deps } = await started({ stack: vi.fn(async () => withServices('app', 'cache')) });
    const pending = gate<StackDetail>();
    deps.stack.mockReturnValueOnce(pending.promise);
    const load = ctl.load();
    ctl.askRemoveService('cache');
    await ctl.confirmRemoveService();
    expect(get(ctl.detail)?.services.map((s) => s.name)).toEqual(['app']);
    pending.resolve(withServices('app', 'cache'));
    await load;
    expect(get(ctl.detail)?.services.map((s) => s.name)).toEqual(['app']);
  });

  it('a 404 on remove toasts a warning, closes the session and reloads', async () => {
    const { ctl, deps } = await started({ stack: vi.fn(async () => withServices('app', 'cache')) });
    deps.removeService.mockRejectedValueOnce(new ApiError(404, 'service not found'));
    ctl.askRemoveService('cache');
    await ctl.confirmRemoveService();
    await settle();
    expect(get(ctl.removing)).toBeUndefined();
    expect(deps.toasts.push).toHaveBeenCalledWith({ kind: 'warning', message: 'service not found' });
    expect(deps.stack).toHaveBeenCalledTimes(2);
  });

  it('a 503 on remove refreshes status and toasts danger, keeping the session open', async () => {
    const { ctl, deps } = await started({ stack: vi.fn(async () => withServices('app', 'cache')) });
    deps.removeService.mockRejectedValueOnce(new ApiError(503, 'docker unavailable'));
    ctl.askRemoveService('cache');
    await ctl.confirmRemoveService();
    expect(deps.refreshStatus).toHaveBeenCalledTimes(1);
    expect(deps.toasts.push).toHaveBeenCalledWith({ kind: 'danger', message: 'docker unavailable' });
    expect(get(ctl.removing)).toMatchObject({ error: 'docker unavailable', pending: false });
    expect(get(ctl.removing)?.disabledReason).toBeUndefined();
  });
});

it('stop cancels the debounce, aborts health and drops late responses', async () => {
  const { ctl, deps, changes, operationsRefresh } = await started();
  const lateStack = gate<StackDetail>();
  const lateHealth = gate<HealthHistory>();
  const lateMetrics = gate<MetricsHistory>();
  deps.stack.mockReturnValueOnce(lateStack.promise);
  deps.health.mockReturnValueOnce(lateHealth.promise);
  deps.metricsHistory.mockReturnValueOnce(lateMetrics.promise);
  const load = ctl.load();
  const health = ctl.loadHealth();
  const cpu = ctl.loadCpuHour('app');
  const healthSignal = deps.health.mock.calls[1][2];
  const metricsSignal = deps.metricsHistory.mock.calls[0][2];
  changes.set({ project: 'web', seq: 1 }); // a refetch is now scheduled

  ctl.stop();
  expect(healthSignal?.aborted).toBe(true);
  expect(metricsSignal?.aborted).toBe(true);

  lateStack.resolve(detail({ hash: 'late' }));
  lateHealth.resolve(history(9999));
  lateMetrics.resolve({ points: [] });
  await Promise.all([load, health, cpu]);
  await vi.advanceTimersByTimeAsync(2000);
  changes.set({ project: '', seq: 2, resync: true });
  operationsRefresh.set(5);
  await settle();

  expect(get(ctl.detail)?.hash).toBe('h1');
  expect(get(ctl.health)).toEqual(history());
  expect(get(ctl.cpuHour).has('app')).toBe(false);
  expect(deps.stack).toHaveBeenCalledTimes(2);
  expect(deps.operations).toHaveBeenCalledTimes(1);
  expect(deps.health).toHaveBeenCalledTimes(2);
  expect(vi.getTimerCount()).toBe(0);
});

it('start after stop resubscribes and loads again (a remounted effect reuses the controller)', async () => {
  const { ctl, deps, changes } = await started();
  ctl.stop();
  ctl.start();
  await settle();
  expect(deps.stack).toHaveBeenCalledTimes(2);
  changes.set({ project: 'web', seq: 1 });
  await vi.advanceTimersByTimeAsync(1500);
  expect(deps.stack).toHaveBeenCalledTimes(3);
});
