// fetch-based SSE client. EventSource cannot send Last-Event-ID on a fresh
// connection, cannot read the status code (so a 401 retries forever) and has
// no heartbeat watchdog, so the stream is read by hand.
import { createParser, type SSEMessage } from './parse';

export type { SSEMessage } from './parse';

export type SSEStatus = 'connecting' | 'open' | 'reconnecting' | 'closed' | 'unauthorized' | 'gone';

export interface SSEOptions {
  url: string;
  /** A function is asked on every connect (first included) and overrides the id tracked internally. */
  lastEventId?: string | (() => string | undefined);
  onMessage(m: SSEMessage): void;
  /** `attempt` is the 0-based backoff exponent used for the pending reconnect. */
  onStatus?(s: SSEStatus, info?: { attempt: number; error?: string }): void;
  /** Silence (no bytes at all, comments included) for this long aborts and reconnects. Default 45000. */
  heartbeatTimeoutMs?: number;
  /** Default 1000..30000. */
  backoff?: { minMs: number; maxMs: number };
  fetchImpl?: typeof fetch;
  now?: () => number;
  setTimeoutImpl?: (fn: () => void, ms: number) => unknown;
  clearTimeoutImpl?: (handle: unknown) => void;
}

export interface SSEHandle {
  close(): void;
  readonly status: SSEStatus;
  readonly lastEventId?: string;
}

const MIN_RETRY_MS = 250;

// Every live handle, so leaving the page releases its connections at once: Chrome allows ~6 HTTP/1.1
// connections per host and keeps a left-behind stream's slot until the server notices.
interface Pausable {
  suspend(): void;
  resume(): void;
}
const handles = new Set<Pausable>();
let pageListeners = false;
function trackPageLifecycle() {
  if (pageListeners || typeof window === 'undefined') return;
  pageListeners = true;
  window.addEventListener('pagehide', () => handles.forEach((h) => h.suspend()));
  // Only a back/forward-cache restore re-shows a page that already ran pagehide.
  window.addEventListener('pageshow', (e) => {
    if ((e as PageTransitionEvent).persisted) handles.forEach((h) => h.resume());
  });
}

export function openSSE(o: SSEOptions): SSEHandle {
  const heartbeatMs = o.heartbeatTimeoutMs ?? 45_000;
  let minMs = o.backoff?.minMs ?? 1000;
  const maxMs = o.backoff?.maxMs ?? 30_000;
  const setT = o.setTimeoutImpl ?? ((fn: () => void, ms: number) => globalThis.setTimeout(fn, ms));
  const clearT = o.clearTimeoutImpl ?? ((h: unknown) => globalThis.clearTimeout(h as number));

  let status: SSEStatus = 'connecting';
  let closed = false;
  let suspended = false;
  let attempt = 0;
  let trackedId = typeof o.lastEventId === 'string' ? o.lastEventId : undefined;
  let controller: AbortController | undefined;
  let heartbeat: unknown;
  let retryTimer: unknown;

  function setStatus(s: SSEStatus, info?: { attempt: number; error?: string }) {
    if (closed) return;
    status = s;
    o.onStatus?.(s, info);
  }

  function stopHeartbeat() {
    if (heartbeat !== undefined) clearT(heartbeat);
    heartbeat = undefined;
  }

  function scheduleReconnect(error: string) {
    if (closed || suspended) return;
    stopHeartbeat();
    const base = Math.min(maxMs, minMs * 2 ** attempt);
    const delay = Math.max(0, base * (1 + (Math.random() * 2 - 1) * 0.2));
    setStatus('reconnecting', { attempt, error });
    if (closed) return;
    attempt = Math.min(attempt + 1, 30);
    retryTimer = setT(() => {
      retryTimer = undefined;
      void connect();
    }, delay);
  }

  async function connect() {
    if (closed || suspended) return;
    const ac = new AbortController();
    controller = ac;
    let timedOut = false;
    const armHeartbeat = () => {
      stopHeartbeat();
      heartbeat = setT(() => {
        timedOut = true;
        ac.abort();
      }, heartbeatMs);
    };

    const headers: Record<string, string> = { Accept: 'text/event-stream' };
    const id = typeof o.lastEventId === 'function' ? o.lastEventId() : trackedId;
    if (id) headers['Last-Event-ID'] = id;

    try {
      armHeartbeat();
      const res = await (o.fetchImpl ?? globalThis.fetch)(o.url, {
        headers,
        credentials: 'same-origin',
        redirect: 'error',
        signal: ac.signal,
      });
      if (closed || ac !== controller) return;
      if (res.status === 401) return terminal('unauthorized');
      if (res.status === 403 || res.status === 404) return terminal('gone');
      if (res.status !== 200 || !res.body) {
        ac.abort();
        return scheduleReconnect(`HTTP ${res.status}`);
      }
      if (!(res.headers.get('content-type') ?? '').toLowerCase().includes('text/event-stream')) {
        // A proxy login page or error page: not our stream, so no `open` and no attempt reset.
        ac.abort();
        return scheduleReconnect(`unexpected content-type "${res.headers.get('content-type') ?? ''}"`);
      }
      attempt = 0;
      armHeartbeat();
      setStatus('open');

      const parser = createParser();
      const decoder = new TextDecoder();
      const reader = res.body.getReader();
      const deliver = (messages: SSEMessage[]) => {
        for (const m of messages) {
          if (closed) return;
          if (m.id !== undefined) trackedId = m.id;
          if (m.retry !== undefined) minMs = Math.max(MIN_RETRY_MS, m.retry);
          // A handler bug must not tear down a healthy stream (or leak it): report and keep reading.
          try {
            o.onMessage(m);
          } catch (e) {
            console.error('SSE onMessage handler threw', e);
          }
        }
      };
      for (;;) {
        const { done, value } = await reader.read();
        if (closed || ac !== controller) return;
        if (done) break;
        armHeartbeat();
        deliver(parser.feed(decoder.decode(value, { stream: true })).messages);
        if (closed) return;
      }
      deliver(parser.flush());
      ac.abort();
      scheduleReconnect('stream ended');
    } catch (err) {
      // Every exit from the read phase releases the connection (HTTP/1.1 allows ~6 per origin).
      ac.abort();
      if (closed || ac !== controller) return;
      scheduleReconnect(timedOut ? 'heartbeat timeout' : err instanceof Error ? err.message : String(err));
    }
  }

  function terminal(s: 'unauthorized' | 'gone') {
    handles.delete(pausable); // finished for good: a bfcache restore must not reconnect it
    stopHeartbeat();
    controller?.abort();
    setStatus(s);
  }

  function close() {
    if (closed) return;
    closed = true;
    status = 'closed';
    handles.delete(pausable);
    stopHeartbeat();
    if (retryTimer !== undefined) clearT(retryTimer);
    retryTimer = undefined;
    controller?.abort();
  }

  function suspend() {
    if (closed || suspended) return;
    suspended = true;
    stopHeartbeat();
    if (retryTimer !== undefined) clearT(retryTimer);
    retryTimer = undefined;
    const ac = controller;
    controller = undefined; // makes the in-flight read/catch paths treat themselves as stale
    ac?.abort();
  }

  function resume() {
    if (closed || !suspended) return;
    suspended = false;
    attempt = 0;
    setStatus('reconnecting', { attempt: 0 });
    void connect();
  }

  // Registered before the first callback can fire: a callback may close the handle synchronously.
  const pausable: Pausable = { suspend, resume };
  handles.add(pausable);
  trackPageLifecycle();
  setStatus('connecting');
  void connect();

  const handle: SSEHandle = {
    close,
    get status() {
      return status;
    },
    get lastEventId() {
      return trackedId;
    },
  };
  return handle;
}
