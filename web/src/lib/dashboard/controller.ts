// Owns the list page's refresh timers (spec §11): host 10 s, summary 30 s,
// alerts 60 s and on every change. Everything stops while the tab is hidden
// and resumes with an immediate fetch when it is visible again.
import { writable, type Readable } from 'svelte/store';
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
  // started is dropped (value and error alike).
  const generation: Record<keyof DashboardErrors, number> = { host: 0, alerts: 0, summary: 0 };
  const invalidateInFlight = () => {
    generation.host++;
    generation.alerts++;
    generation.summary++;
  };

  // A failed fetch keeps the previous value; the next tick retries.
  async function load<T>(key: keyof DashboardErrors, fetcher: () => Promise<T>, target: { set(v: T): void }) {
    const mine = ++generation[key];
    try {
      const value = await fetcher();
      if (mine !== generation[key]) return;
      target.set(value);
      errorStore.update((e) => ({ ...e, [key]: undefined }));
    } catch (err) {
      if (mine !== generation[key]) return;
      const message = err instanceof Error ? err.message : String(err);
      errorStore.update((e) => ({ ...e, [key]: message }));
    }
  }
  const fetchHost = () => load('host', deps.host, hostStore);
  const fetchAlerts = () => load('alerts', deps.alerts, alertsStore);
  const fetchSummary = () => load('summary', deps.summary, summaryStore);

  const alertsOnChange = debounce(() => void fetchAlerts());

  function startTimers() {
    timers = [
      setInterval(() => void fetchHost(), HOST_MS),
      setInterval(() => void fetchSummary(), SUMMARY_MS),
      setInterval(() => void fetchAlerts(), ALERTS_MS),
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
