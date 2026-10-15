// Controller for the metrics tab: history (JSON) + live (SSE) merged into one
// SeriesSet. Kept free of Svelte components so the lifecycle is testable.
import { get, writable, derived, type Readable, type Writable } from 'svelte/store';
import type { MetricsHistory } from '../api/types';
import type { openSSE, SSEStatus } from '../sse/client';
import { createStackLiveMetrics, type StackLiveMetrics } from './liveMetrics';
import {
  appendLive,
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

export const COLOR_SLOTS = 8;

/** Colour slot of a service: its position in the sorted list of services, wrapping after 8. */
export function serviceColorIndex(service: string, all: string[]): number {
  const i = [...all].sort().indexOf(service);
  return (i < 0 ? 0 : i) % COLOR_SLOTS;
}

export interface MetricsDeps {
  history: (q: { from: string; to: string; step: string }, signal?: AbortSignal) => Promise<MetricsHistory>;
  open?: typeof openSSE;
  /** A shared source (one stream per page); without it the controller owns one built from `open`. */
  live?: StackLiveMetrics;
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
  const now = deps.now ?? (() => new Date());
  const live = deps.live ?? createStackLiveMetrics(stackName, { open: deps.open });
  const ownsLive = deps.live === undefined;
  const range = writable<Range>('1h');
  const seriesStore = writable<SeriesSet>(new Map());
  const degraded = writable(false);

  let history: SeriesSet = new Map();
  let liveSet: SeriesSet = new Map();
  let abort: AbortController | undefined;
  let generation = 0;
  let closed = false;
  // Points that arrive while a history load is in flight are dropped (they would be older than, or overlap, the history being fetched; a short gap remains).
  let accepting = false;
  let dirty = false;

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

  const releaseLive = live.acquire();
  const offPoints = live.onPoints((service, points) => {
    if (!accepting || closed) return;
    const windowMs = RANGE_MS[get(range)];
    for (const p of points) appendLive(liveSet, service, p, windowMs);
    dirty = dirty || points.length > 0;
  });
  // `latest` is set once per message after every service's points, so a multi-service message publishes once.
  const offLatest = live.latest.subscribe(() => {
    if (!dirty) return;
    dirty = false;
    publish();
  });

  async function reload(): Promise<void> {
    abort?.abort();
    accepting = false;
    dirty = false;
    loaded.set(false);
    const gen = ++generation;
    const ctl = (abort = new AbortController());
    const q = rangeToQuery(get(range), now());
    liveSet = new Map();
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
    accepting = true;
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
    status: live.status,
    degraded: { subscribe: degraded.subscribe },
    loaded: { subscribe: loaded.subscribe },
    reload,
    close() {
      closed = true;
      generation++;
      abort?.abort();
      accepting = false;
      offPoints();
      offLatest();
      releaseLive();
      if (ownsLive) live.close();
      unsubscribeRange();
    },
  };
}
