import { writable, type Readable } from 'svelte/store';
import { createCoalescer, createLogBuffer, type LogBuffer } from '../logs/buffer';
import type { LogError, LogEvent } from '../api/types';
import { openSSE, type SSEHandle, type SSEMessage, type SSEStatus } from '../sse/client';

export interface LogStreamOptions {
  service?: string;
  tail: number | 'all';
  follow: boolean;
  since?: string;
}

export interface LogStreamDeps {
  open?: typeof openSSE;
  buffer?: LogBuffer;
  /** Frame scheduler for the coalescer (tests inject a microtask). */
  schedule?: (cb: FrameRequestCallback) => number;
  /** Buffer/coalescer capacity; default 5000. */
  capacity?: number;
}

export interface LogStream {
  buffer: LogBuffer;
  status: Readable<SSEStatus>;
  errors: Readable<LogError[]>;
  ended: Readable<boolean>;
  /** Lines lost to buffer eviction plus lines trimmed while the tab was hidden. */
  dropped: Readable<number>;
  restart(opts: LogStreamOptions): void;
  close(): void;
  /** Empties the buffer and the dropped count; the stream and cursors keep going. */
  clear(): void;
  /** Composite Last-Event-ID: the latest id of every container, comma-joined. */
  cursors(): string;
}

// eslint-disable-next-line no-control-regex
const ANSI = /\u001b\[[0-9;?]*[ -/]*[@-~]/g;

const CURSOR_ID = /^([0-9a-f]+):(\d+):(\d+)$/;

/** "<container>:<ts>:<ordinal>" -> cursors[container] = id. Malformed ids are ignored. */
export function mergeCursor(cursors: Map<string, string>, id: string): void {
  const m = CURSOR_ID.exec(id);
  if (m) cursors.set(m[1], id);
}

function buildUrl(stackName: string, o: LogStreamOptions): string {
  const q = new URLSearchParams();
  if (o.service) q.set('service', o.service);
  q.set('tail', String(o.tail));
  if (o.since) q.set('since', o.since);
  if (o.follow) q.set('follow', '1');
  return `/api/stacks/${encodeURIComponent(stackName)}/logs?${q.toString()}`;
}

function parse<T>(m: SSEMessage): T | undefined {
  try {
    return JSON.parse(m.data) as T;
  } catch {
    return undefined;
  }
}

export function createLogStream(stackName: string, initial: LogStreamOptions, deps: LogStreamDeps = {}): LogStream {
  const open = deps.open ?? openSSE;
  const buffer = deps.buffer ?? createLogBuffer(deps.capacity);
  const status = writable<SSEStatus>('connecting');
  const errors = writable<LogError[]>([]);
  const ended = writable(false);
  const dropped = writable(0);
  const cursorMap = new Map<string, string>();
  let trimmedTotal = 0;
  let handle: SSEHandle | undefined;
  let generation = 0;
  let closed = false;

  const syncDropped = () => dropped.set(buffer.dropped() + trimmedTotal);

  const coalescer = createCoalescer<LogEvent>(
    (items, trimmed) => {
      trimmedTotal += trimmed;
      buffer.push(items);
      syncDropped();
    },
    deps.schedule,
    deps.capacity,
  );

  function start(opts: LogStreamOptions) {
    const gen = ++generation;
    let h: SSEHandle | undefined;
    h = open({
      url: buildUrl(stackName, opts),
      // Asked on every connect: a reconnect resumes every container exactly, with no `since`.
      lastEventId: () => {
        const c = [...cursorMap.values()].join(',');
        return c === '' ? undefined : c;
      },
      onStatus: (s) => {
        if (gen === generation) status.set(s);
      },
      onMessage: (m) => {
        if (gen !== generation) return;
        if (m.event === 'log') {
          const ev = parse<LogEvent>(m);
          if (!ev) return;
          if (m.id) mergeCursor(cursorMap, m.id);
          // Strip ANSI once here so filtering and highlighting see the same text.
          coalescer.add({ ...ev, text: ev.text.replace(ANSI, '') });
        } else if (m.event === 'error') {
          const err = parse<LogError>(m);
          if (!err) return;
          errors.update((list) => [...list.filter((e) => e.container !== err.container), err]);
        } else if (m.event === 'end') {
          ended.set(true);
          // The server closes after `end`; do not let the client reconnect.
          h?.close();
          status.set('closed');
        }
      },
    });
    handle = h;
  }

  function stop() {
    generation++;
    handle?.close();
    handle = undefined;
    coalescer.cancel();
  }

  start(initial);

  return {
    buffer,
    status,
    errors,
    ended,
    dropped,
    clear() {
      coalescer.cancel();
      trimmedTotal = 0;
      buffer.clear();
      syncDropped();
    },
    cursors: () => [...cursorMap.values()].join(','),
    restart(opts) {
      if (closed) return;
      stop();
      cursorMap.clear();
      buffer.clear();
      trimmedTotal = 0;
      errors.set([]);
      ended.set(false);
      status.set('connecting');
      syncDropped();
      start(opts);
    },
    close() {
      if (closed) return;
      closed = true;
      stop(); // stream first, then cancel (add after cancel re-arms the coalescer)
      status.set('closed');
    },
  };
}
