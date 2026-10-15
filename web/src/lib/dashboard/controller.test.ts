import { get, writable } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Alert, HostSnapshot, MetricsSummary } from '../api/types';
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

const host = { hostname: 'h' } as HostSnapshot;
const summary = { window: '15m' } as MetricsSummary;
const alertList: Alert[] = [];

let doc: FakeDoc;
let changes: ReturnType<typeof writable<LiveChange | null>>;
let fetchHost: ReturnType<typeof vi.fn<() => Promise<HostSnapshot>>>;
let fetchAlerts: ReturnType<typeof vi.fn<() => Promise<Alert[]>>>;
let fetchSummary: ReturnType<typeof vi.fn<() => Promise<MetricsSummary>>>;

function make() {
  return createDashboard({
    host: fetchHost,
    alerts: fetchAlerts,
    summary: fetchSummary,
    changes,
    doc: doc as unknown as Document,
  });
}
const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

beforeEach(() => {
  vi.useFakeTimers();
  doc = new FakeDoc();
  changes = writable<LiveChange | null>(null);
  fetchHost = vi.fn(async () => host);
  fetchAlerts = vi.fn(async () => alertList);
  fetchSummary = vi.fn(async () => summary);
});
afterEach(() => vi.useRealTimers());

describe('dashboard controller', () => {
  it('no timers before start', async () => {
    make();
    await tick(120_000);
    expect(vi.getTimerCount()).toBe(0);
    expect(fetchHost).not.toHaveBeenCalled();
  });

  it('fetches all three on start', async () => {
    const d = make();
    d.start();
    await tick();
    expect(fetchHost).toHaveBeenCalledTimes(1);
    expect(fetchAlerts).toHaveBeenCalledTimes(1);
    expect(fetchSummary).toHaveBeenCalledTimes(1);
    expect(get(d.host)).toBe(host);
    expect(get(d.alerts)).toBe(alertList);
    expect(get(d.summary)).toBe(summary);
    d.stop();
  });

  it('host every 10s summary every 30s alerts every 60s', async () => {
    const d = make();
    d.start();
    await tick();
    await tick(60_000);
    expect(fetchHost).toHaveBeenCalledTimes(1 + 6);
    expect(fetchSummary).toHaveBeenCalledTimes(1 + 2);
    expect(fetchAlerts).toHaveBeenCalledTimes(1 + 1);
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
    expect(fetchHost).toHaveBeenCalledTimes(1);
    expect(fetchAlerts).toHaveBeenCalledTimes(1);
    doc.setVisibility('visible');
    await tick();
    expect(fetchHost).toHaveBeenCalledTimes(2);
    expect(fetchAlerts).toHaveBeenCalledTimes(2);
    expect(fetchSummary).toHaveBeenCalledTimes(2);
    await tick(10_000);
    expect(fetchHost).toHaveBeenCalledTimes(3);
    d.stop();
  });

  it('failed fetch keeps previous value and records error', async () => {
    const d = make();
    d.start();
    await tick();
    fetchHost.mockRejectedValueOnce(new Error('boom'));
    await tick(10_000);
    expect(get(d.host)).toBe(host);
    expect(get(d.errors).host).toBe('boom');
    await tick(10_000);
    expect(get(d.errors).host).toBeUndefined();
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
    expect(fetchHost).toHaveBeenCalledTimes(1);
    expect(fetchAlerts).toHaveBeenCalledTimes(1);
  });
});
