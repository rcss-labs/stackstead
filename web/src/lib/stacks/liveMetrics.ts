// One ref-counted live metrics stream per stack page, shared by everything that
// shows "now" values (the aside and the Metrics tab). Free of Svelte components.
import { writable, type Readable } from 'svelte/store';
import type { Sample } from '../api/types';
import { createLiveAggregator, type SeriesPoint } from '../metrics/series';
import { openSSE, type SSEHandle, type SSEStatus } from '../sse/client';

/** The aggregator emits a tick only when the next one starts; flush after this much silence. */
const IDLE_FLUSH_MS = 1200;
const RECONNECT_DELAY_MS = 1000;

export interface StackLiveMetrics {
  status: Readable<SSEStatus>;
  /** Newest aggregated point per service (rates present once two samples exist). */
  latest: Readable<Map<string, SeriesPoint>>;
  /** Every aggregated point as it is produced; returns an unsubscribe. */
  onPoints(fn: (service: string, points: SeriesPoint[]) => void): () => void;
  /** Opens the stream on the first holder, closes it after the last release. */
  acquire(): () => void;
  close(): void;
}

export function createStackLiveMetrics(stackName: string, deps: { open?: typeof openSSE } = {}): StackLiveMetrics {
  const open = deps.open ?? openSSE;
  const status = writable<SSEStatus>('connecting');
  const latest = writable<Map<string, SeriesPoint>>(new Map());
  const listeners = new Set<(service: string, points: SeriesPoint[]) => void>();

  // One aggregator per service: aggregator output carries no service name.
  let aggregators = new Map<string, ReturnType<typeof createLiveAggregator>>();
  let newest = new Map<string, SeriesPoint>();
  let holders = 0;
  let handle: SSEHandle | undefined;
  let idleTimer: ReturnType<typeof setTimeout> | undefined;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  // Bumped whenever the stream is stopped, so a pending reconnect can tell it is stale.
  let epoch = 0;

  /** Hands points to listeners and records the newest; returns whether anything was produced. */
  function emit(service: string, points: SeriesPoint[]): boolean {
    if (points.length === 0) return false;
    newest.set(service, points[points.length - 1]);
    for (const fn of [...listeners]) fn(service, points);
    return true;
  }

  /** `latest` is set once per batch, after every listener has seen the batch's points. */
  function publishLatest() {
    latest.set(new Map(newest));
  }

  function flushAll() {
    let changed = false;
    for (const [service, agg] of aggregators) changed = emit(service, agg.flush()) || changed;
    if (changed) publishLatest();
  }

  function onMessage(m: { event: string; data: string }) {
    if (m.event === 'dropped') {
      handle?.close();
      handle = undefined;
      clearTimeout(reconnectTimer);
      const gen = epoch;
      reconnectTimer = setTimeout(() => {
        if (holders > 0 && gen === epoch) connect();
      }, RECONNECT_DELAY_MS);
      return;
    }
    if (m.event !== 'sample') return;
    let parsed: Sample | Sample[];
    try {
      parsed = JSON.parse(m.data);
    } catch {
      return;
    }
    const samples = Array.isArray(parsed) ? parsed : [parsed];
    let changed = false;
    for (const s of samples) {
      let agg = aggregators.get(s.service);
      if (!agg) aggregators.set(s.service, (agg = createLiveAggregator()));
      changed = emit(s.service, agg.ingest([s])) || changed;
    }
    if (changed) publishLatest();
    clearTimeout(idleTimer);
    idleTimer = setTimeout(flushAll, IDLE_FLUSH_MS);
  }

  function connect() {
    handle = open({
      url: `/api/stacks/${encodeURIComponent(stackName)}/metrics/live`,
      onMessage,
      onStatus: (s) => status.set(s),
    });
  }

  function stop() {
    epoch++;
    handle?.close();
    handle = undefined;
    clearTimeout(idleTimer);
    clearTimeout(reconnectTimer);
    aggregators = new Map();
    newest = new Map();
    latest.set(new Map());
  }

  return {
    status: { subscribe: status.subscribe },
    latest: { subscribe: latest.subscribe },
    onPoints(fn) {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    acquire() {
      holders++;
      if (holders === 1) connect();
      let released = false;
      return () => {
        if (released) return;
        released = true;
        holders--;
        if (holders === 0) stop();
      };
    },
    close() {
      holders = 0;
      stop();
    },
  };
}

const sumOf = (values: (number | undefined)[]): number | undefined => {
  const present = values.filter((v): v is number => v !== undefined);
  return present.length === 0 ? undefined : present.reduce((a, b) => a + b, 0);
};

/** Stack-wide "now" values: cpu/mem over all services, rates over the services that have them. */
export function totalsNow(latest: Map<string, SeriesPoint>): { cpu?: number; mem?: number; netRx?: number; netTx?: number } {
  const pts = [...latest.values()];
  return {
    cpu: sumOf(pts.map((p) => p.cpu)),
    mem: sumOf(pts.map((p) => p.mem)),
    netRx: sumOf(pts.map((p) => p.netRx)),
    netTx: sumOf(pts.map((p) => p.netTx)),
  };
}
