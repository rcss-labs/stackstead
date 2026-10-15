// Multiplexer for /api/events. One shared connection feeds status flags and a
// change signal; screens refetch on every `resync` because events missed
// while disconnected are not replayed.
import { readable, writable, type Readable } from 'svelte/store';
import type { Status } from '../api/types';
import { openSSE, type SSEHandle, type SSEStatus } from '../sse/client';
import type { SSEMessage } from '../sse/parse';

export interface LiveState {
  connection: SSEStatus;
  events: 'connected' | 'disconnected' | 'unknown';
  metricsDegraded: boolean;
  docker: 'ok' | 'unavailable' | 'unknown';
  compose: string | 'unavailable' | 'unknown';
  /** Engine version; '' = unknown. */
  dockerVersion: string;
  metricsIntervalSeconds?: number;
  metricsRetentionSeconds?: number;
}

export interface LiveChange {
  project: string;
  seq: number;
  resync?: true;
}

const initial: LiveState = {
  connection: 'closed',
  events: 'unknown',
  metricsDegraded: false,
  docker: 'unknown',
  compose: 'unknown',
  dockerVersion: '',
};

const state = writable<LiveState>({ ...initial });
const changeStore = writable<LiveChange | null>(null);

export const live: Readable<LiveState> = { subscribe: state.subscribe };
export const changes: Readable<LiveChange | null> = { subscribe: changeStore.subscribe };

interface Deps {
  fetchStatus: () => Promise<Status>;
  open?: typeof openSSE;
}

let deps: Deps | null = null;
let handle: SSEHandle | null = null;
let generation = 0;
let statusSeq = 0;
let openedAt = 0;
/** A dropped this soon after open means the server cannot keep up: back off instead of looping. */
const DROPPED_LOOP_MS = 5000;

export function startLive(d: Deps): void {
  if (handle) return;
  deps = d;
  connect();
}

function connect(): void {
  const gen = ++generation;
  const open = deps?.open ?? openSSE;
  const current = open({
    url: '/api/events',
    onMessage: (m) => {
      if (gen === generation) handleMessage(m);
    },
    onStatus: (s) => {
      if (gen === generation) handleStatus(s);
    },
  });
  // Callbacks may already have fired synchronously and torn this connection down.
  if (gen === generation) handle = current;
  else current.close();
}

function handleStatus(s: SSEStatus): void {
  if (s === 'unauthorized' || s === 'gone') state.set({ ...initial, connection: s });
  else state.update((v) => ({ ...v, connection: s }));
  if (s === 'open') {
    openedAt = Date.now();
    changeStore.set({ project: '', seq: 0, resync: true });
    void refreshStatus();
  } else if (s === 'unauthorized' || s === 'gone') {
    // The client is already finished; drop it so a later startLive (after login) opens a fresh one.
    teardown();
  }
}

function handleMessage(m: SSEMessage): void {
  switch (m.event) {
    case 'status': {
      const flags = parse<{ events?: string; metricsDegraded?: boolean }>(m.data);
      if (!flags) return;
      let flipped = false;
      state.update((v) => {
        const next = flags.events === 'connected' || flags.events === 'disconnected' ? flags.events : v.events;
        flipped = v.events !== 'unknown' && next !== 'unknown' && next !== v.events;
        return { ...v, events: next, metricsDegraded: flags.metricsDegraded ?? v.metricsDegraded };
      });
      if (flipped) void refreshStatus();
      return;
    }
    case 'change': {
      const c = parse<{ project?: string; seq?: number }>(m.data);
      if (c && typeof c.project === 'string' && typeof c.seq === 'number') {
        changeStore.set({ project: c.project, seq: c.seq });
      }
      return;
    }
    case 'dropped': {
      // The server ended a slow client. Reconnect now, unless we only just opened:
      // then let the client's own backoff handle the stream ending.
      if (Date.now() - openedAt < DROPPED_LOOP_MS) return;
      teardown();
      connect();
      return;
    }
  }
}

function parse<T>(data: string): T | null {
  try {
    return JSON.parse(data) as T;
  } catch {
    return null;
  }
}

function teardown(): void {
  generation++;
  handle?.close();
  handle = null;
}

export function stopLive(): void {
  teardown();
  // Logout or teardown: forget flags so the next session never sees the last one's state.
  state.set({ ...initial });
  changeStore.set(null);
  statusSeq++;
  deps = null;
}

export async function refreshStatus(): Promise<void> {
  const fetchStatus = deps?.fetchStatus;
  if (!fetchStatus) return;
  const seq = ++statusSeq;
  try {
    const s = await fetchStatus();
    if (seq !== statusSeq) return; // stopped, or a newer refresh is in flight
    state.update((v) => ({
      ...v,
      docker: s.docker,
      compose: s.compose,
      dockerVersion: s.dockerVersion ?? '',
      metricsIntervalSeconds: s.metricsIntervalSeconds,
      metricsRetentionSeconds: s.metricsRetentionSeconds,
    }));
  } catch {
    // Keep the last known flags; the next open or flip tries again.
  }
}
