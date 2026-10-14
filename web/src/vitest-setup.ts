// Test harness: a recording fake `fetch` and a controllable SSE stream. No
// network, no msw. Import the installers from tests; the file itself only
// registers a cleanup so components mounted by a test never leak.
import { cleanup } from '@testing-library/svelte';
import { afterEach } from 'vitest';

afterEach(() => cleanup());

export interface RecordedCall {
  method: string;
  /** Path plus query string, without origin. */
  path: string;
  headers: Headers;
  /** JSON-parsed body, the raw string if it is not JSON, or undefined. */
  body: unknown;
  signal?: AbortSignal | null;
}

export interface FakeResponse {
  status?: number;
  body?: unknown;
  headers?: Record<string, string>;
}

export type RouteHandler = (call: RecordedCall) => FakeResponse | Promise<FakeResponse>;
/** Keys are "METHOD /path" (path without query unless the key includes one). */
export type Routes = Record<string, FakeResponse | RouteHandler>;

function splitRequest(input: RequestInfo | URL, init?: RequestInit) {
  const req = input instanceof Request ? input : undefined;
  const raw = req ? req.url : input instanceof URL ? input.href : String(input);
  const url = new URL(raw, 'http://localhost');
  return {
    method: (init?.method ?? req?.method ?? 'GET').toUpperCase(),
    path: url.pathname + url.search,
    pathname: url.pathname,
    headers: new Headers(init?.headers ?? req?.headers),
    signal: init?.signal ?? req?.signal,
    rawBody: init?.body,
  };
}

function parseBody(raw: BodyInit | null | undefined): unknown {
  if (typeof raw !== 'string') return undefined;
  try {
    return JSON.parse(raw);
  } catch {
    return raw;
  }
}

export interface FakeFetch {
  calls: RecordedCall[];
  restore: () => void;
}

export function installFakeFetch(routes: Routes): FakeFetch {
  const original = globalThis.fetch;
  const calls: RecordedCall[] = [];
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const r = splitRequest(input, init);
    const call: RecordedCall = {
      method: r.method,
      path: r.path,
      headers: r.headers,
      body: parseBody(r.rawBody),
      signal: r.signal,
    };
    calls.push(call);
    const route = routes[`${r.method} ${r.path}`] ?? routes[`${r.method} ${r.pathname}`];
    if (!route) return jsonResponse({ status: 404, body: { error: 'no fake route' } });
    const resp = typeof route === 'function' ? await route(call) : route;
    return jsonResponse(resp);
  }) as typeof fetch;
  return {
    calls,
    restore() {
      globalThis.fetch = original;
    },
  };
}

function jsonResponse(r: FakeResponse): Response {
  const status = r.status ?? 200;
  const headers = new Headers(r.headers);
  const noBody = status === 204 || status === 304 || r.body === undefined;
  if (!noBody && !headers.has('content-type')) headers.set('content-type', 'application/json');
  return new Response(noBody ? null : typeof r.body === 'string' ? r.body : JSON.stringify(r.body), {
    status,
    headers,
  });
}

export interface FakeStreamRequest {
  path: string;
  headers: Headers;
  signal?: AbortSignal | null;
}

export interface FakeStream {
  /** Every request made to a fake stream URL, in order (reconnects included). */
  requests: FakeStreamRequest[];
  /** Enqueue text on the most recent open stream for `path`. */
  push(path: string, chunk: string): void;
  /** End the most recent stream for `path` normally. */
  close(path: string): void;
  /** Fail the most recent stream for `path`. */
  error(path: string, err: unknown): void;
  restore: () => void;
}

/** Paths map to the chunks delivered as soon as a client connects. */
export function installFakeStream(initial: Record<string, string[]>): FakeStream {
  const original = globalThis.fetch;
  const requests: FakeStreamRequest[] = [];
  const controllers = new Map<string, ReadableStreamDefaultController<Uint8Array>>();
  const enc = new TextEncoder();

  const live = (path: string) => {
    const c = controllers.get(path);
    if (!c) throw new Error(`no open fake stream for ${path}`);
    return c;
  };

  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const r = splitRequest(input, init);
    const chunks = initial[r.pathname];
    if (!chunks) return jsonResponse({ status: 404, body: { error: 'no fake stream' } });
    requests.push({ path: r.path, headers: r.headers, signal: r.signal });
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controllers.set(r.pathname, controller);
        for (const chunk of chunks) controller.enqueue(enc.encode(chunk));
        r.signal?.addEventListener('abort', () => {
          try {
            controller.error(r.signal?.reason ?? new DOMException('aborted', 'AbortError'));
          } catch {
            /* already closed */
          }
        });
      },
    });
    return new Response(body, { status: 200, headers: { 'content-type': 'text/event-stream' } });
  }) as typeof fetch;

  return {
    requests,
    push: (path, chunk) => live(path).enqueue(enc.encode(chunk)),
    close: (path) => {
      live(path).close();
      controllers.delete(path);
    },
    error: (path, err) => {
      live(path).error(err);
      controllers.delete(path);
    },
    restore() {
      globalThis.fetch = original;
    },
  };
}
