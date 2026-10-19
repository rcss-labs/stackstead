import { get, writable } from '$lib/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Alert, MetricsSummary, StorageOverview } from '../api/types';
import type { LiveChange } from '../stores/live';
import { createDashboard } from './controller';

class FakeDoc extends EventTarget {
  visibilityState: 'visible' | 'hidden' = 'visible';
  listeners = 0;
  addEventListener(...a: Parameters<EventTarget['addEventListener']>) {
    this.listeners++;
    super.addEventListener(...a);
  }
  removeEventListener(...a: Parameters<EventTarget['removeEventListener']>) {
    this.listeners--;
    super.removeEventListener(...a);
  }
  setVisibility(v: 'visible' | 'hidden') {
    this.visibilityState = v;
    this.dispatchEvent(new Event('visibilitychange'));
  }
}

const storage = { reclaimable: 1 } as unknown as StorageOverview;
const summary = { window: '15m' } as MetricsSummary;
const alertList: Alert[] = [];

let doc: FakeDoc;
let changes: ReturnType<typeof writable<LiveChange | null>>;
let fetchStorage: ReturnType<typeof vi.fn<() => Promise<StorageOverview>>>;
let fetchAlerts: ReturnType<typeof vi.fn<() => Promise<Alert[]>>>;
let fetchSummary: ReturnType<typeof vi.fn<() => Promise<MetricsSummary>>>;

function make(withStorage = true) {
  return createDashboard({
    alerts: fetchAlerts,
    summary: fetchSummary,
    ...(withStorage ? { storage: fetchStorage } : {}),
    changes,
    doc: doc as unknown as Document,
  });
}
const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

beforeEach(() => {
  vi.useFakeTimers();
  doc = new FakeDoc();
  changes = writable<LiveChange | null>(null);
  fetchStorage = vi.fn(async () => storage);
  fetchAlerts = vi.fn(async () => alertList);
  fetchSummary = vi.fn(async () => summary);
});
afterEach(() => vi.useRealTimers());

describe('dashboard controller', () => {
  it('no timers before start', async () => {
    make();
    await tick(120_000);
    expect(vi.getTimerCount()).toBe(0);
    expect(fetchAlerts).not.toHaveBeenCalled();
  });

  it('fetches all three on start', async () => {
    const d = make();
    d.start();
    await tick();
    expect(fetchAlerts).toHaveBeenCalledTimes(1);
    expect(fetchSummary).toHaveBeenCalledTimes(1);
    expect(fetchStorage).toHaveBeenCalledTimes(1);
    expect(get(d.storage)).toBe(storage);
    expect(get(d.alerts)).toBe(alertList);
    expect(get(d.summary)).toBe(summary);
    d.stop();
  });

  it('summary every 30s alerts every 60s', async () => {
    const d = make();
    d.start();
    await tick();
    await tick(60_000);
    expect(fetchSummary).toHaveBeenCalledTimes(1 + 2);
    expect(fetchAlerts).toHaveBeenCalledTimes(1 + 1);
    expect(fetchStorage).toHaveBeenCalledTimes(1 + 1);
    d.stop();
  });

  it('alerts refetch on change debounced and immediately on resync', async () => {
    const d = make();
    d.start();
    await tick();
    // start() subscribes to changes; the initial null must not trigger a fetch.
    expect(fetchAlerts).toHaveBeenCalledTimes(1);
    changes.set({ project: 'a', seq: 1 });
    changes.set({ project: 'a', seq: 2 });
    await tick(299);
    expect(fetchAlerts).toHaveBeenCalledTimes(1);
    await tick(1);
    expect(fetchAlerts).toHaveBeenCalledTimes(2);
    changes.set({ project: '', seq: 0, resync: true });
    await tick();
    expect(fetchAlerts).toHaveBeenCalledTimes(3);
    d.stop();
  });

  it('pauses all timers while hidden and fetches once on visible', async () => {
    const d = make();
    d.start();
    await tick();
    changes.set({ project: 'a', seq: 1 }); // pending debounce
    doc.setVisibility('hidden');
    expect(vi.getTimerCount()).toBe(0);
    await tick(120_000);
    expect(fetchSummary).toHaveBeenCalledTimes(1);
    expect(fetchAlerts).toHaveBeenCalledTimes(1);
    expect(fetchStorage).toHaveBeenCalledTimes(1);
    doc.setVisibility('visible');
    await tick();
    expect(fetchAlerts).toHaveBeenCalledTimes(2);
    expect(fetchSummary).toHaveBeenCalledTimes(2);
    expect(fetchStorage).toHaveBeenCalledTimes(2);
    await tick(30_000);
    expect(fetchSummary).toHaveBeenCalledTimes(3);
    d.stop();
  });

  it('failed fetch keeps previous value and records error', async () => {
    const d = make();
    d.start();
    await tick();
    fetchSummary.mockRejectedValueOnce(new Error('boom'));
    await tick(30_000);
    expect(get(d.summary)).toBe(summary);
    expect(get(d.errors).summary).toBe('boom');
    await tick(30_000);
    expect(get(d.errors).summary).toBeUndefined();
    d.stop();
  });

  it('stop clears timers and listener', async () => {
    const d = make();
    d.start();
    await tick();
    changes.set({ project: 'a', seq: 1 });
    expect(doc.listeners).toBe(1);
    d.stop();
    expect(doc.listeners).toBe(0);
    expect(vi.getTimerCount()).toBe(0);
    changes.set({ project: 'a', seq: 2 });
    await tick(120_000);
    expect(fetchSummary).toHaveBeenCalledTimes(1);
    expect(fetchAlerts).toHaveBeenCalledTimes(1);
    expect(fetchStorage).toHaveBeenCalledTimes(1);
  });

  it('storage is never fetched without a storage dep', async () => {
    const d = make(false);
    d.start();
    await tick(120_000);
    doc.setVisibility('hidden');
    doc.setVisibility('visible');
    await tick();
    expect(fetchStorage).not.toHaveBeenCalled();
    expect(get(d.storage)).toBeUndefined();
    expect(get(d.errors).storage).toBeUndefined();
    d.stop();
  });

  it('storage fetches on start, every 60 s and on visible, but not on a change event', async () => {
    const d = make();
    d.start();
    await tick();
    expect(fetchStorage).toHaveBeenCalledTimes(1);
    changes.set({ project: 'a', seq: 1 });
    changes.set({ project: '', seq: 0, resync: true });
    await tick(1_000);
    expect(fetchStorage).toHaveBeenCalledTimes(1);
    await tick(59_000);
    expect(fetchStorage).toHaveBeenCalledTimes(2);
    doc.setVisibility('hidden');
    doc.setVisibility('visible');
    await tick();
    expect(fetchStorage).toHaveBeenCalledTimes(3);
    d.stop();
  });

  it('a storage failure records errors.storage and keeps the value', async () => {
    const d = make();
    d.start();
    await tick();
    fetchStorage.mockRejectedValueOnce(new Error('no docker'));
    await tick(60_000);
    expect(get(d.storage)).toBe(storage);
    expect(get(d.errors).storage).toBe('no docker');
    await tick(60_000);
    expect(get(d.errors).storage).toBeUndefined();
    d.stop();
  });

  it('refreshAlerts fetches alerts at once and is a no-op before start and after stop', async () => {
    const d = make();
    d.refreshAlerts();
    await tick();
    expect(fetchAlerts).not.toHaveBeenCalled();
    d.start();
    await tick();
    expect(fetchAlerts).toHaveBeenCalledTimes(1);
    d.refreshAlerts();
    await tick();
    expect(fetchAlerts).toHaveBeenCalledTimes(2);
    d.stop();
    d.refreshAlerts();
    await tick();
    expect(fetchAlerts).toHaveBeenCalledTimes(2);
  });

  describe('stale responses', () => {
    function deferred<T>() {
      let resolve!: (v: T) => void;
      let reject!: (e: Error) => void;
      const promise = new Promise<T>((res, rej) => ((resolve = res), (reject = rej)));
      return { promise, resolve, reject };
    }
    const alertsA = [{ kind: 'unhealthy', severity: 'danger', stack: 'a', message: 'a', since: '' }] as Alert[];
    const alertsB = [] as Alert[];

    // A change event forces a refetch even while one is in flight, so it can overtake.
    it('an older slow response does not overwrite a newer one', async () => {
      const older = deferred<Alert[]>();
      const newer = deferred<Alert[]>();
      fetchAlerts.mockReset().mockReturnValueOnce(older.promise).mockReturnValueOnce(newer.promise);
      const d = make();
      d.start();
      await tick();
      changes.set({ project: '', seq: 0, resync: true });
      await tick();
      newer.resolve(alertsB);
      await tick();
      older.resolve(alertsA);
      await tick();
      expect(get(d.alerts)).toBe(alertsB);
      d.stop();
    });

    it('a late failure does not re-record an error after a newer success', async () => {
      const older = deferred<Alert[]>();
      const newer = deferred<Alert[]>();
      fetchAlerts.mockReset().mockReturnValueOnce(older.promise).mockReturnValueOnce(newer.promise);
      const d = make();
      d.start();
      await tick();
      changes.set({ project: '', seq: 0, resync: true });
      await tick();
      newer.resolve(alertsB);
      await tick();
      older.reject(new Error('late'));
      await tick();
      expect(get(d.errors).alerts).toBeUndefined();
      expect(get(d.alerts)).toBe(alertsB);
      d.stop();
    });
  });
});
