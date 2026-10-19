// Owns the list page's refresh timers (spec §11): host 10 s, summary 30 s,
// alerts 60 s and on every change. Everything stops while the tab is hidden
// and resumes with an immediate fetch when it is visible again.
import { writable, type Readable } from '$lib/store';
import type { Alert, HostSnapshot, MetricsSummary } from '../api/types';
import { debounce } from '../stores/debounce';
import type { LiveChange } from '../stores/live';

export interface DashboardDeps {
  host: () => Promise<HostSnapshot>;
  alerts: () => Promise<Alert[]>;
  summary: () => Promise<MetricsSummary>;
  changes: Readable<LiveChange | null>;
  doc?: Document;
}

export interface DashboardErrors {
  host?: string;
  alerts?: string;
  summary?: string;
}

const HOST_MS = 10_000;
const SUMMARY_MS = 30_000;
const ALERTS_MS = 60_000;

export function createDashboard(deps: DashboardDeps) {
  const doc = deps.doc ?? document;
  const hostStore = writable<HostSnapshot | undefined>(undefined);
  const alertsStore = writable<Alert[] | undefined>(undefined);
  const summaryStore = writable<MetricsSummary | undefined>(undefined);
  const errorStore = writable<DashboardErrors>({});

  let started = false;
  let timers: ReturnType<typeof setInterval>[] = [];
  let unsubscribeChanges: (() => void) | undefined;

  // Per-key generation: bumped when a fetch starts and on stop/hide, so a response that
  // arrives after stop, after the tab went hidden, or after a newer fetch of the same key
  // started is dropped (value and error alike). Interval ticks skip a key that already has
  // a fetch in flight, so a slow endpoint still lands instead of being overtaken forever.
  const generation: Record<keyof DashboardErrors, number> = { host: 0, alerts: 0, summary: 0 };
  const busy: Record<keyof DashboardErrors, boolean> = { host: false, alerts: false, summary: false };
  const invalidateInFlight = () => {
    for (const key of Object.keys(generation) as (keyof DashboardErrors)[]) {
      generation[key]++;
      busy[key] = false;
    }
  };

  // A failed fetch keeps the previous value; the next tick retries.
  async function load<T>(key: keyof DashboardErrors, fetcher: () => Promise<T>, target: { set(v: T): void }, interval = false) {
    if (interval && busy[key]) return;
    const mine = ++generation[key];
    busy[key] = true;
    try {
      const value = await fetcher();
      if (mine !== generation[key]) return;
      target.set(value);
      errorStore.update((e) => ({ ...e, [key]: undefined }));
    } catch (err) {
      if (mine !== generation[key]) return;
      const message = err instanceof Error ? err.message : String(err);
      errorStore.update((e) => ({ ...e, [key]: message }));
    } finally {
      if (mine === generation[key]) busy[key] = false;
    }
  }
  const fetchHost = (interval = false) => load('host', deps.host, hostStore, interval);
  const fetchAlerts = (interval = false) => load('alerts', deps.alerts, alertsStore, interval);
  const fetchSummary = (interval = false) => load('summary', deps.summary, summaryStore, interval);

  const alertsOnChange = debounce(() => void fetchAlerts());

  function startTimers() {
    timers = [
      setInterval(() => void fetchHost(true), HOST_MS),
      setInterval(() => void fetchSummary(true), SUMMARY_MS),
      setInterval(() => void fetchAlerts(true), ALERTS_MS),
    ];
  }

  function stopTimers() {
    invalidateInFlight();
    timers.forEach(clearInterval);
    timers = [];
    alertsOnChange.cancel();
  }

  function fetchAll() {
    void fetchHost();
    void fetchAlerts();
    void fetchSummary();
  }

  function onVisibility() {
    if (doc.visibilityState === 'hidden') {
      stopTimers();
    } else {
      stopTimers();
      fetchAll();
      startTimers();
    }
  }

  function onChange(c: LiveChange | null) {
    if (!c || doc.visibilityState === 'hidden') return;
    if (c.resync) alertsOnChange.flush();
    else alertsOnChange.trigger();
  }

  return {
    host: { subscribe: hostStore.subscribe } as Readable<HostSnapshot | undefined>,
    alerts: { subscribe: alertsStore.subscribe } as Readable<Alert[] | undefined>,
    summary: { subscribe: summaryStore.subscribe } as Readable<MetricsSummary | undefined>,
    errors: { subscribe: errorStore.subscribe } as Readable<DashboardErrors>,
    start() {
      if (started) return;
      started = true;
      doc.addEventListener('visibilitychange', onVisibility);
      if (doc.visibilityState !== 'hidden') {
        fetchAll();
        startTimers();
      }
      // Subscribing replays the current value; skip it so start() fetches alerts once.
      let first = true;
      unsubscribeChanges = deps.changes.subscribe((c) => {
        if (first) {
          first = false;
          return;
        }
        onChange(c);
      });
    },
    stop() {
      if (!started) return;
      started = false;
      stopTimers();
      unsubscribeChanges?.();
      unsubscribeChanges = undefined;
      doc.removeEventListener('visibilitychange', onVisibility);
    },
  };
}
