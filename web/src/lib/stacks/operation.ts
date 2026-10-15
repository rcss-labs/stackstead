// Store behind OperationPanel: follows /api/operations/{id}/stream, resuming
// with the absolute line index as Last-Event-ID so no line repeats or is lost.
import { writable, type Readable } from 'svelte/store';
import { api } from '$lib/api/client';
import type { Operation, OperationDetail } from '$lib/api/types';
import { openSSE, type SSEHandle, type SSEMessage, type SSEStatus } from '$lib/sse/client';

/** Matches the server's per-operation retention. */
export const MAX_LINES = 20_000;
/** Trim in chunks so a busy stream does not re-slice the buffer on every line. */
const TRIM_SLACK = 1_000;

export interface OutputEntry {
  /** 'note' is the "earlier lines dropped" header, 'gap' marks lines missed across a resume. */
  kind: 'line' | 'note' | 'gap';
  /** Stable, unique within the stream: use it as the keyed-each key. */
  key: string;
  text: string;
}

export interface OperationStream {
  lines: Readable<OutputEntry[]>;
  op: Readable<Operation | undefined>;
  status: Readable<SSEStatus>;
  /** True once the stream is gone and the operation could not be fetched either. */
  unavailable: Readable<boolean>;
  close(): void;
}

export interface OperationStreamDeps {
  open?: typeof openSSE;
  get?: (id: string) => Promise<OperationDetail>;
}

const DROPPED_NOTE = /^\.\.\. (\d+) earlier lines dropped$/;

export function createOperationStream(id: string, deps: OperationStreamDeps = {}): OperationStream {
  const open = deps.open ?? openSSE;
  const get = deps.get ?? api.operation;
  const url = `/api/operations/${encodeURIComponent(id)}/stream`;

  const linesStore = writable<OutputEntry[]>([]);
  const opStore = writable<Operation | undefined>(undefined);
  const statusStore = writable<SSEStatus>('connecting');
  const unavailableStore = writable(false);

  let body: OutputEntry[] = []; // lines and gap markers
  let lineCount = 0;
  let serverNote: string | undefined;
  let clientDropped = 0;
  let lastIndex = -1;
  let finished = false;
  let closed = false;
  let current: SSEHandle | undefined;
  let finalFetched = false;
  let publishScheduled = false;

  function noteText(): string | undefined {
    if (clientDropped === 0) return serverNote;
    const fromServer = Number(DROPPED_NOTE.exec(serverNote ?? '')?.[1] ?? 0);
    return `... ${fromServer + clientDropped} earlier lines dropped`;
  }

  function publishNow() {
    publishScheduled = false;
    if (closed) return;
    const note = noteText();
    linesStore.set(note === undefined ? body.slice() : [{ kind: 'note', key: 'note', text: note }, ...body]);
  }

  // Batched: a 20k-line replay arrives inside one task, so it publishes once.
  function publish() {
    if (publishScheduled) return;
    publishScheduled = true;
    queueMicrotask(publishNow);
  }

  function trim() {
    if (lineCount <= MAX_LINES + TRIM_SLACK) return;
    const drop = lineCount - MAX_LINES;
    let cut = 0;
    let removedLines = 0;
    while (cut < body.length && removedLines < drop) {
      if (body[cut].kind === 'line') removedLines++;
      cut++;
    }
    clientDropped += removedLines;
    lineCount -= removedLines;
    body = body.slice(cut);
  }

  function finish(op: Operation) {
    finished = true;
    opStore.set(op);
    current?.close();
    publishNow();
    statusStore.set('closed');
  }

  function onMessage(m: SSEMessage) {
    if (finished) return;
    if (m.event === 'line') {
      let text: string;
      try {
        const parsed: unknown = JSON.parse(m.data);
        text = typeof parsed === 'string' ? parsed : m.data;
      } catch {
        text = m.data;
      }
      if (m.id === undefined) {
        // The "N earlier lines dropped" note stands in for evicted lines and has no id.
        // If we already hold early lines (a resume) the note would mislead; the gap marker covers it.
        if (lastIndex < 0 && body.length === 0) {
          serverNote = text;
          publish();
        }
        return;
      }
      const index = Number(m.id);
      if (Number.isInteger(index)) {
        if (index <= lastIndex) return;
        if (lastIndex >= 0 && index > lastIndex + 1) {
          const missed = index - lastIndex - 1;
          body.push({ kind: 'gap', key: `g${index}`, text: `… ${missed} lines missed` });
        }
        lastIndex = index;
      }
      body.push({ kind: 'line', key: `l${Number.isInteger(index) ? index : body.length}`, text });
      lineCount++;
      trim();
      publish();
    } else if (m.event === 'done') {
      try {
        finish(JSON.parse(m.data) as Operation);
      } catch {
        void fetchFinal();
      }
    } else if (m.event === 'dropped') {
      // We fell behind: reconnect now (not after backoff), resuming after the last line seen.
      current?.close();
      connect();
    }
  }

  function onStatus(s: SSEStatus) {
    if (finished || closed) return;
    statusStore.set(s);
    if (s === 'gone' || s === 'unauthorized') void fetchFinal();
  }

  // The stream ended without a usable `done`: show the operation's final state once.
  async function fetchFinal() {
    if (finalFetched || closed) return;
    finalFetched = true;
    current?.close();
    try {
      const detail = await get(id);
      if (closed) return;
      if (body.length === 0 && detail.lines.length > 0) {
        body = detail.lines.map((text, i) => ({ kind: 'line', key: `d${i}`, text }));
        lineCount = body.length;
      }
      const { lines: _lines, ...op } = detail;
      finished = true;
      opStore.set(op);
      publishNow();
    } catch {
      let hasOp = false;
      opStore.update((cur) => ((hasOp = cur !== undefined), cur));
      if (!hasOp) unavailableStore.set(true);
    }
  }

  function connect() {
    if (closed || finished) return;
    current = open({
      url,
      lastEventId: () => (lastIndex >= 0 ? String(lastIndex) : undefined),
      onMessage,
      onStatus,
    });
  }

  connect();
  // Header metadata (action, stack, start time) while the stream is still running.
  get(id)
    .then((detail) => {
      if (closed) return;
      const { lines: _lines, ...op } = detail;
      opStore.update((cur) => cur ?? op);
    })
    .catch(() => {});

  return {
    lines: { subscribe: linesStore.subscribe },
    op: { subscribe: opStore.subscribe },
    status: { subscribe: statusStore.subscribe },
    unavailable: { subscribe: unavailableStore.subscribe },
    close() {
      closed = true;
      current?.close();
    },
  };
}

/** Display-only tone for an output line: docker compose success marks and error wording. */
export function outputTone(line: string): 'success' | 'danger' | undefined {
  if (line.includes('✔')) return 'success';
  if (line.includes('✘') || line.includes('Error') || line.includes('error:') || line.includes('failed')) return 'danger';
  return undefined;
}

export type StateTone = 'success' | 'warning' | 'danger' | 'muted';

/** Tone and label for an operation state, shared by the list rows and the panel header. */
export function operationStateLook(state: string): { tone: StateTone; label: string } {
  switch (state) {
    case 'running':
    case 'succeeded':
      return { tone: 'success', label: state };
    case 'failed':
      return { tone: 'danger', label: state };
    case 'timed_out':
      return { tone: 'warning', label: 'timed out' };
    default:
      return { tone: 'muted', label: state.replace('_', ' ') };
  }
}

/** Milliseconds an operation ran (up to `now` while running); undefined if the timestamps do not parse. */
export function operationDurationMs(op: Pick<Operation, 'startedAt' | 'endedAt'>, now: number): number | undefined {
  const start = Date.parse(op.startedAt);
  const end = op.endedAt ? Date.parse(op.endedAt) : now;
  return Number.isNaN(start) || Number.isNaN(end) ? undefined : Math.max(0, end - start);
}
