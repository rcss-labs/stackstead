// Store behind OperationPanel: follows /api/operations/{id}/stream, resuming
// with the absolute line index as Last-Event-ID so no line repeats or is lost.
import { writable, type Readable } from 'svelte/store';
import { api } from '$lib/api/client';
import type { Operation, OperationDetail } from '$lib/api/types';
import { openSSE, type SSEHandle, type SSEMessage, type SSEStatus } from '$lib/sse/client';

/** Matches the server's per-operation retention. */
export const MAX_LINES = 20_000;

export interface OperationStream {
  lines: Readable<string[]>;
  op: Readable<Operation | undefined>;
  status: Readable<SSEStatus>;
  close(): void;
}

export interface OperationStreamDeps {
  open?: typeof openSSE;
  get?: (id: string) => Promise<OperationDetail>;
}

export function createOperationStream(id: string, deps: OperationStreamDeps = {}): OperationStream {
  const open = deps.open ?? openSSE;
  const get = deps.get ?? api.operation;
  const url = `/api/operations/${encodeURIComponent(id)}/stream`;

  const linesStore = writable<string[]>([]);
  const opStore = writable<Operation | undefined>(undefined);
  const statusStore = writable<SSEStatus>('connecting');

  let body: string[] = [];
  let note: string | undefined;
  let lastIndex = -1;
  let finished = false;
  let closed = false;
  let current: SSEHandle | undefined;
  let finalFetched = false;

  const publish = () => linesStore.set(note === undefined ? body : [note, ...body]);

  function finish(op: Operation) {
    finished = true;
    opStore.set(op);
    current?.close();
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
        note = text;
        publish();
        return;
      }
      const index = Number(m.id);
      if (Number.isInteger(index)) {
        if (index <= lastIndex) return;
        lastIndex = index;
      }
      body.push(text);
      if (body.length > MAX_LINES) body = body.slice(body.length - MAX_LINES);
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

  // The stream ended without `done`: show the operation's final state once.
  async function fetchFinal() {
    if (finalFetched || closed) return;
    finalFetched = true;
    try {
      const detail = await get(id);
      if (closed) return;
      if (body.length === 0 && detail.lines.length > 0) {
        body = detail.lines;
        publish();
      }
      const { lines: _lines, ...op } = detail;
      finished = true;
      opStore.set(op);
    } catch {
      /* the connection status already tells the user what happened */
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
    close() {
      closed = true;
      current?.close();
    },
  };
}

