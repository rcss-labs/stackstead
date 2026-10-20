// Per-server metrics summary and host snapshot for the All stacks screen, polled on the fleet's cadence
// while the tab is visible. Each server answers on its own: one failing never blanks another.
import { writable, type Readable } from '$lib/store';
import type { HostSnapshot, MetricsSummary } from '../api/types';
import { HOST_POLL_INTERVAL_MS } from '../stores/host';

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

/** Same cadences as the dashboard: summaries every 30 s, the host every 10 s. */
export const SUMMARY_POLL_INTERVAL_MS = 30_000;

export function createFleetStats(deps: {
  loadSummary: (id: string) => Promise<MetricsSummary>;
  loadHost: (id: string) => Promise<HostSnapshot>;
  /** False for a server whose host the caller already has (this server's, from the shell's host store). */
  hostOf?: (id: string) => boolean;
  doc?: Document;
}): FleetStats {
  const doc = deps.doc ?? document;
  const wantsHost = deps.hostOf ?? (() => true);
  const state = writable<FleetStatsState>({});
  let ids: string[] = [];
  let timers: ReturnType<typeof setInterval>[] = [];
  let running = false;
  let generation = 0;
  // One request of each kind per server at a time: a slow peer must not pile requests up.
  const inFlight = new Set<string>();

  const blank: ServerStats = { summary: undefined, host: undefined, hostError: '' };
  const patch = (id: string, change: Partial<ServerStats>) => state.update((s) => ({ ...s, [id]: { ...(s[id] ?? blank), ...change } }));

  async function refresh(kind: 'summary' | 'host', id: string): Promise<void> {
    const key = `${kind}/${id}`;
    if (inFlight.has(key)) return;
    inFlight.add(key);
    const mine = generation;
    try {
      const value = await (kind === 'summary' ? deps.loadSummary(id) : deps.loadHost(id));
      if (mine === generation && ids.includes(id)) patch(id, kind === 'summary' ? { summary: value as MetricsSummary } : { host: value as HostSnapshot, hostError: '' });
    } catch (e) {
      if (mine === generation && ids.includes(id)) {
        patch(id, kind === 'summary' ? { summary: undefined } : { host: undefined, hostError: e instanceof Error ? e.message : String(e) });
      }
    } finally {
      if (mine === generation) inFlight.delete(key);
    }
  }

  const refreshSummaries = () => ids.forEach((id) => void refresh('summary', id));
  const refreshHosts = () => ids.filter(wantsHost).forEach((id) => void refresh('host', id));
  const pause = () => {
    timers.forEach(clearInterval);
    timers = [];
  };
  const resume = () => {
    pause();
    refreshSummaries();
    refreshHosts();
    timers = [setInterval(refreshSummaries, SUMMARY_POLL_INTERVAL_MS), setInterval(refreshHosts, HOST_POLL_INTERVAL_MS)];
  };
  const onVisibility = () => (doc.visibilityState === 'hidden' ? pause() : resume());

  return {
    state: { subscribe: state.subscribe },
    setServers(next) {
      const added = next.filter((id) => !ids.includes(id));
      ids = next;
      state.update((s) => Object.fromEntries(Object.entries(s).filter(([id]) => next.includes(id))));
      if (running && timers.length > 0) {
        for (const id of added) {
          void refresh('summary', id);
          if (wantsHost(id)) void refresh('host', id);
        }
      }
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
      inFlight.clear();
    },
  };
}
