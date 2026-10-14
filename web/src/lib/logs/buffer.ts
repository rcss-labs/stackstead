import type { Readable } from 'svelte/store';
import type { LogEvent } from '../api/types';

export interface LogLine extends LogEvent {
  seq: number;
}

export interface LogFilter {
  service?: string;
  stream?: 'stdout' | 'stderr';
  search?: string;
}

const DEFAULT_CAP = 5000;

export interface LogBuffer {
  push(lines: LogEvent[]): void;
  snapshot(): LogLine[];
  dropped(): number;
  clear(): void;
  subscribe: Readable<LogLine[]>['subscribe'];
}

// Fixed-size circular array: a flood overwrites the oldest slot in O(1)
// instead of paying Array.shift's O(n) per line.
export function createLogBuffer(cap = DEFAULT_CAP): LogBuffer {
  const capacity = Math.max(1, Math.floor(Number.isFinite(cap) ? cap : DEFAULT_CAP));
  const slots: (LogLine | undefined)[] = new Array(capacity);
  let head = 0; // index of the oldest line
  let size = 0;
  let droppedCount = 0;
  let nextSeq = 1; // never reset, so seq stays usable as a keyed-each key across clear()
  const subscribers = new Set<(value: LogLine[]) => void>();

  function snapshot(): LogLine[] {
    const out = new Array<LogLine>(size);
    for (let i = 0; i < size; i++) out[i] = slots[(head + i) % capacity] as LogLine;
    return out;
  }

  function notify(): void {
    if (subscribers.size === 0) return;
    const value = snapshot();
    for (const fn of [...subscribers]) fn(value);
  }

  return {
    push(lines) {
      if (lines.length === 0) return;
      // Lines that would be evicted by the same push are only counted, not built.
      let start = 0;
      if (lines.length > capacity) {
        start = lines.length - capacity;
        const skipped = start;
        nextSeq += skipped;
        // everything currently stored is evicted too
        droppedCount += skipped + size;
        head = 0;
        size = 0;
        slots.fill(undefined);
      }
      for (let i = start; i < lines.length; i++) {
        const entry: LogLine = { ...lines[i], seq: nextSeq++ };
        if (size < capacity) {
          slots[(head + size) % capacity] = entry;
          size++;
        } else {
          slots[head] = entry;
          head = (head + 1) % capacity;
          droppedCount++;
        }
      }
      notify();
    },
    snapshot,
    dropped: () => droppedCount,
    clear() {
      slots.fill(undefined);
      head = 0;
      size = 0;
      droppedCount = 0;
      notify();
    },
    subscribe(run) {
      run(snapshot());
      subscribers.add(run);
      return () => {
        subscribers.delete(run);
      };
    },
  };
}

export function filterLines(lines: LogLine[], f: LogFilter): LogLine[] {
  const needle = f.search ? f.search.toLowerCase() : '';
  if (!f.service && !f.stream && !needle) return lines;
  return lines.filter(
    (l) =>
      (!f.service || l.service === f.service) &&
      (!f.stream || l.stream === f.stream) &&
      (!needle || l.text.toLowerCase().includes(needle)),
  );
}

type Schedule = (cb: FrameRequestCallback) => number;

// Batches adds and flushes once per frame; flush also receives how many of the
// oldest items were trimmed (pending is capped at maxPending, so a hidden tab
// where rAF is paused cannot accumulate an unbounded backlog). The default
// scheduler is resolved lazily so importing this module needs no browser global.
//
// Note: add() after cancel() re-arms the coalescer. Callers must close their
// stream before (or together with) cancel() on unmount.
export function createCoalescer<T>(
  flush: (items: T[], trimmed: number) => void,
  schedule: Schedule = (cb) => requestAnimationFrame(cb),
  maxPending = DEFAULT_CAP,
): { add(item: T): void; add(items: T[]): void; cancel(): void } {
  const max = Math.max(1, Math.floor(Number.isFinite(maxPending) ? maxPending : DEFAULT_CAP));
  let pending: T[] = [];
  let trimmed = 0;
  let generation = 0;
  let scheduled = false;

  function trim(): void {
    if (pending.length > max) {
      trimmed += pending.length - max;
      pending = pending.slice(pending.length - max);
    }
  }

  function run(gen: number): void {
    if (gen !== generation) return; // cancelled after scheduling
    scheduled = false;
    trim();
    const items = pending;
    const lost = trimmed;
    pending = [];
    trimmed = 0;
    if (items.length > 0) flush(items, lost);
  }

  return {
    add(itemOrItems: T | T[]) {
      if (Array.isArray(itemOrItems)) for (const it of itemOrItems) pending.push(it);
      else pending.push(itemOrItems);
      // Amortised: trimming on every add would be O(max) per line.
      if (pending.length > 2 * max) trim();
      if (!scheduled) {
        scheduled = true;
        const gen = generation;
        schedule(() => run(gen));
      }
    },
    cancel() {
      pending = [];
      trimmed = 0;
      generation++;
      scheduled = false;
    },
  };
}
