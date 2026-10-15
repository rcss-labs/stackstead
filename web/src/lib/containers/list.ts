// Pure helpers for the Containers list plus the controller that keeps it fresh (spec §5.2):
// refetch on every live change (debounced) and every 10 s while the tab is visible, nothing while hidden.
import { writable, type Readable } from 'svelte/store';
import type { ContainerRow } from '../api/types';
import { debounce } from '../stores/debounce';
import type { LiveChange } from '../stores/live';

export type ContainerChip = 'all' | 'running' | 'stopped' | 'problems' | 'unmanaged';

const REFRESH_MS = 10_000;

export function isProblem(c: ContainerRow): boolean {
  if (c.state === 'restarting' || c.state === 'dead' || c.health === 'unhealthy') return true;
  return c.state === 'exited' && c.exitCode !== null && c.exitCode !== 0;
}

const rank = (c: ContainerRow) => (isProblem(c) ? 0 : c.state === 'running' ? 1 : 2);

export function sortContainers(rows: ContainerRow[]): ContainerRow[] {
  return [...rows].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
}

export function filterContainers(rows: ContainerRow[], chip: ContainerChip, query: string): ContainerRow[] {
  const q = query.trim().toLowerCase();
  return rows.filter((c) => {
    if (chip === 'running' && c.state !== 'running') return false;
    if (chip === 'stopped' && c.state === 'running') return false;
    if (chip === 'problems' && !isProblem(c)) return false;
    if (chip === 'unmanaged' && c.managed === 'stack') return false;
    if (!q) return true;
    return [c.name, c.image, c.stack, c.project, ...c.ports].some((field) => field.toLowerCase().includes(q));
  });
}

export function chipCounts(rows: ContainerRow[]): Record<ContainerChip, number> {
  const counts: Record<ContainerChip, number> = { all: rows.length, running: 0, stopped: 0, problems: 0, unmanaged: 0 };
  for (const c of rows) {
    if (c.state === 'running') counts.running++;
    else counts.stopped++;
    if (isProblem(c)) counts.problems++;
    if (c.managed !== 'stack') counts.unmanaged++;
  }
  return counts;
}

export interface ContainerListDeps {
  list: () => Promise<ContainerRow[]>;
  changes: Readable<LiveChange | null>;
  doc?: Document;
}

export function createContainerList(deps: ContainerListDeps) {
  const doc = deps.doc ?? document;
  const rowsStore = writable<ContainerRow[] | undefined>(undefined);
  const errorStore = writable<string | undefined>(undefined);

  let started = false;
  let timer: ReturnType<typeof setInterval> | undefined;
  let unsubscribeChanges: (() => void) | undefined;

  // Generation guard: a response that arrives after stop, hide, or a newer fetch is dropped.
  // Interval ticks skip while a fetch is in flight so a slow server still gets to answer.
  let generation = 0;
  let busy = false;

  async function load(interval = false) {
    if (interval && busy) return;
    const mine = ++generation;
    busy = true;
    try {
      const rows = await deps.list();
      if (mine !== generation) return;
      rowsStore.set(rows);
      errorStore.set(undefined);
    } catch (err) {
      if (mine !== generation) return;
      errorStore.set(err instanceof Error ? err.message : String(err));
    } finally {
      if (mine === generation) busy = false;
    }
  }

  const onChangeFetch = debounce(() => void load());

  function stopTimers() {
    generation++;
    busy = false;
    clearInterval(timer);
    timer = undefined;
    onChangeFetch.cancel();
  }

  function startTimers() {
    timer = setInterval(() => void load(true), REFRESH_MS);
  }

  function onVisibility() {
    stopTimers();
    if (doc.visibilityState === 'hidden') return;
    void load();
    startTimers();
  }

  return {
    rows: { subscribe: rowsStore.subscribe } as Readable<ContainerRow[] | undefined>,
    error: { subscribe: errorStore.subscribe } as Readable<string | undefined>,
    /** Apply a row the server just returned (an action's result) and drop any fetch that started before it. */
    patch(fresh: ContainerRow) {
      generation++;
      busy = false;
      rowsStore.update((rows) => rows?.map((r) => (r.id === fresh.id ? fresh : r)));
    },
    /** Fetch now (Retry, or after an action changed a row). */
    refresh() {
      if (started && doc.visibilityState !== 'hidden') void load();
    },
    start() {
      if (started) return;
      started = true;
      doc.addEventListener('visibilitychange', onVisibility);
      if (doc.visibilityState !== 'hidden') {
        void load();
        startTimers();
      }
      // Subscribing replays the current value; skip it so start() fetches once.
      let first = true;
      unsubscribeChanges = deps.changes.subscribe((c) => {
        if (first) {
          first = false;
          return;
        }
        if (!c || doc.visibilityState === 'hidden') return;
        if (c.resync) onChangeFetch.flush();
        else onChangeFetch.trigger();
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
