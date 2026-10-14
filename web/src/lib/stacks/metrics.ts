// Controller for the metrics tab: history (JSON) + live (SSE) merged into one
// SeriesSet. Kept free of Svelte components so the lifecycle is testable.
import { get, writable, derived, type Readable, type Writable } from 'svelte/store';
import type { MetricsHistory, Sample } from '../api/types';
import { openSSE, type SSEHandle, type SSEStatus } from '../sse/client';
import {
  appendLive,
  createLiveAggregator,
  historyToSeries,
  joinHistoryAndLive,
  rangeToQuery,
  type Range,
  type SeriesPoint,
  type SeriesSet,
} from '../metrics/series';

export const RANGE_MS: Record<Range, number> = {
  '1h': 3_600_000,
  '6h': 6 * 3_600_000,
  '24h': 24 * 3_600_000,
  '72h': 72 * 3_600_000,
};

/** The aggregator emits a tick only when the next one starts; flush after this much silence. */
const IDLE_FLUSH_MS = 1200;
const RECONNECT_DELAY_MS = 1000;
export const COLOR_SLOTS = 8;

/** Colour slot of a service: its position in the sorted list of services, wrapping after 8. */
export function serviceColorIndex(service: string, all: string[]): number {
  const i = [...all].sort().indexOf(service);
  return (i < 0 ? 0 : i) % COLOR_SLOTS;
}

export interface MetricsDeps {
  history: (q: { from: string; to: string; step: string }, signal?: AbortSignal) => Promise<MetricsHistory>;
  open?: typeof openSSE;
  now?: () => Date;
}

export interface MetricsController {
  series: Readable<SeriesSet>;
  range: Writable<Range>;
  services: Readable<string[]>;
  status: Readable<SSEStatus>;
  /** True when the last history request failed (live data may still flow). */
  degraded: Readable<boolean>;
  /** False until the latest (non-superseded) history load has finished. */
  loaded: Readable<boolean>;
  reload(): Promise<void>;
  close(): void;
}

const isAbort = (e: unknown, signal: AbortSignal) => signal.aborted || (e as { name?: string })?.name === 'AbortError';

export function createMetricsController(stackName: string, deps: MetricsDeps): MetricsController {
  const open = deps.open ?? openSSE;
  const now = deps.now ?? (() => new Date());
  const range = writable<Range>('1h');
  const seriesStore = writable<SeriesSet>(new Map());
  const status = writable<SSEStatus>('connecting');
  const degraded = writable(false);

  let history: SeriesSet = new Map();
  let liveSet: SeriesSet = new Map();
  // One aggregator per service: aggregator output carries no service name.
  let aggregators = new Map<string, ReturnType<typeof createLiveAggregator>>();
  let abort: AbortController | undefined;
  let handle: SSEHandle | undefined;
  let idleTimer: ReturnType<typeof setTimeout> | undefined;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  let generation = 0;
  let closed = false;

  const loaded = writable(false);

  /** Joins history and live and trims everything older than the range window behind the newest point. */
  function publish() {
    const joined = joinHistoryAndLive(history, liveSet);
    let newest = -Infinity;
    for (const pts of joined.values()) if (pts.length > 0) newest = Math.max(newest, pts[pts.length - 1].t);
    const cutoff = newest - RANGE_MS[get(range)];
    for (const [service, pts] of joined) {
      let drop = 0;
      while (drop < pts.length && pts[drop].t < cutoff) drop++;
      if (drop > 0) joined.set(service, pts.slice(drop));
    }
    seriesStore.set(joined);
  }

  function stopStream() {
    handle?.close();
    handle = undefined;
    clearTimeout(idleTimer);
    clearTimeout(reconnectTimer);
  }

  /** Appends points without publishing; returns whether anything was added. */
  function emit(service: string, points: SeriesPoint[]): boolean {
    const windowMs = RANGE_MS[get(range)];
    for (const p of points) appendLive(liveSet, service, p, windowMs);
    return points.length > 0;
  }

  function flushAll() {
    let changed = false;
    for (const [service, agg] of aggregators) changed = emit(service, agg.flush()) || changed;
    if (changed) publish();
  }

  function onMessage(m: { event: string; data: string }) {
    if (m.event === 'dropped') {
      handle?.close();
      handle = undefined;
      clearTimeout(reconnectTimer);
      const gen = generation;
      reconnectTimer = setTimeout(() => {
        if (!closed && gen === generation) connect();
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
    if (changed) publish();
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

  async function reload(): Promise<void> {
    abort?.abort();
    stopStream();
    loaded.set(false);
    const gen = ++generation;
    const ctl = (abort = new AbortController());
    const q = rangeToQuery(get(range), now());
    liveSet = new Map();
    aggregators = new Map();
    try {
      const res = await deps.history(q, ctl.signal);
      if (ctl.signal.aborted || closed) return;
      history = historyToSeries(res.points);
      degraded.set(false);
    } catch (e) {
      if (isAbort(e, ctl.signal) || closed) return;
      history = new Map();
      degraded.set(true);
    }
    if (gen !== generation) return;
    publish();
    loaded.set(true);
    connect();
  }

  let first = true;
  const unsubscribeRange = range.subscribe(() => {
    if (first) {
      first = false;
      return;
    }
    void reload();
  });

  return {
    series: { subscribe: seriesStore.subscribe },
    range,
    services: derived(seriesStore, (s) => [...s.keys()].sort()),
    status: { subscribe: status.subscribe },
    degraded: { subscribe: degraded.subscribe },
    loaded: { subscribe: loaded.subscribe },
    reload,
    close() {
      closed = true;
      generation++;
      abort?.abort();
      stopStream();
      unsubscribeRange();
    },
  };
}
