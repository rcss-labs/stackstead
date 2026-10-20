// Per-server metrics summary and host snapshot for the All stacks screen, polled on the fleet's cadence
// while the tab is visible. Each server answers on its own: one failing never blanks another.
import { writable, type Readable } from '$lib/store';
import type { HostSnapshot, MetricsSummary } from '../api/types';
import { FLEET_POLL_INTERVAL_MS } from './fleet';

export interface ServerStats {
  summary: MetricsSummary | undefined;
  host: HostSnapshot | undefined;
  /** Set when the last host request failed. */
  hostError: string;
}

export type FleetStatsState = Record<string, ServerStats>;

export interface FleetStats {
  state: Readable<FleetStatsState>;
  /** The reachable servers to poll; a new one is loaded at once, a gone one is forgotten. */
  setServers(ids: string[]): void;
  start(): void;
  stop(): void;
}

export function createFleetStats(deps: {
  loadSummary: (id: string) => Promise<MetricsSummary>;
  loadHost: (id: string) => Promise<HostSnapshot>;
  doc?: Document;
}): FleetStats {
  const doc = deps.doc ?? document;
  const state = writable<FleetStatsState>({});
  let ids: string[] = [];
  let timer: ReturnType<typeof setInterval> | undefined;
  let running = false;
  let generation = 0;

  async function refreshOne(id: string, mine: number): Promise<void> {
    const [summary, host] = await Promise.allSettled([deps.loadSummary(id), deps.loadHost(id)]);
    if (mine !== generation || !ids.includes(id)) return;
    state.update((s) => ({
      ...s,
      [id]: {
        summary: summary.status === 'fulfilled' ? summary.value : undefined,
        host: host.status === 'fulfilled' ? host.value : undefined,
        hostError: host.status === 'rejected' ? (host.reason instanceof Error ? host.reason.message : String(host.reason)) : '',
      },
    }));
  }

  const refreshAll = () => {
    const mine = generation;
    for (const id of ids) void refreshOne(id, mine);
  };
  const pause = () => {
    clearInterval(timer);
    timer = undefined;
  };
  const resume = () => {
    pause();
    refreshAll();
    timer = setInterval(refreshAll, FLEET_POLL_INTERVAL_MS);
  };
  const onVisibility = () => (doc.visibilityState === 'hidden' ? pause() : resume());

  return {
    state: { subscribe: state.subscribe },
    setServers(next) {
      const added = next.filter((id) => !ids.includes(id));
      ids = next;
      state.update((s) => Object.fromEntries(Object.entries(s).filter(([id]) => next.includes(id))));
      if (running && timer !== undefined) for (const id of added) void refreshOne(id, generation);
    },
    start() {
      if (running) return;
      running = true;
      doc.addEventListener('visibilitychange', onVisibility);
      if (doc.visibilityState !== 'hidden') resume();
    },
    stop() {
      if (!running) return;
      running = false;
      pause();
      doc.removeEventListener('visibilitychange', onVisibility);
      generation++; // an in-flight response must not land after stop
    },
  };
}
