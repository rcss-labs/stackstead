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

/** One scripted answer. Without `status` (or with 200) it is an open SSE stream delivering `chunks`. */
export interface StreamResponse {
  status?: number;
  headers?: Record<string, string>;
  /** Text delivered as soon as the client connects (200 only). */
  chunks?: string[];
  /** JSON body for non-200 answers. */
  body?: unknown;
}

/** A path maps to initial chunks (every request gets a 200 stream) or to a script consumed one response per request. */
export type StreamRoutes = Record<string, string[] | { responses: StreamResponse[] }>;

export interface FakeStream {
  /** Every request made to a fake stream URL, in order (reconnects included). */
  requests: FakeStreamRequest[];
  /** Enqueue text on the open stream for `path`. Streams are keyed by path: the newest wins. */
  push(path: string, chunk: string): void;
  /** End the newest stream for `path` normally. */
  close(path: string): void;
  /** Fail the newest stream for `path`. */
  error(path: string, err: unknown): void;
  restore: () => void;
}

/**
 * Fakes SSE endpoints. Paths not listed pass through to the fetch that was
 * installed before (e.g. installFakeFetch). A scripted path answers 404 once
 * its responses are used up.
 */
export function installFakeStream(routes: StreamRoutes): FakeStream {
  const original = globalThis.fetch;
  const requests: FakeStreamRequest[] = [];
  const controllers = new Map<string, ReadableStreamDefaultController<Uint8Array>>();
  const cursors = new Map<string, number>();
  const enc = new TextEncoder();

  const live = (path: string) => {
    const c = controllers.get(path);
    if (!c) throw new Error(`no open fake stream for ${path}`);
    return c;
  };

  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const r = splitRequest(input, init);
    const route = routes[r.pathname];
    if (!route) return original(input, init);
    // Like real fetch: a signal that is already aborted rejects before anything is sent.
    if (r.signal?.aborted) throw r.signal.reason ?? new DOMException('aborted', 'AbortError');
    requests.push({ path: r.path, headers: r.headers, signal: r.signal });

    let response: StreamResponse;
    if (Array.isArray(route)) {
      response = { chunks: route };
    } else {
      const n = cursors.get(r.pathname) ?? 0;
      cursors.set(r.pathname, n + 1);
      const scripted = route.responses[n];
      if (!scripted) return jsonResponse({ status: 404, body: { error: 'fake stream script exhausted' } });
      response = scripted;
    }
    const status = response.status ?? 200;
    if (status !== 200) return jsonResponse({ status, body: response.body, headers: response.headers });

    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controllers.set(r.pathname, controller);
        for (const chunk of response.chunks ?? []) controller.enqueue(enc.encode(chunk));
        r.signal?.addEventListener('abort', () => {
          if (controllers.get(r.pathname) === controller) controllers.delete(r.pathname);
          try {
            controller.error(r.signal?.reason ?? new DOMException('aborted', 'AbortError'));
          } catch {
            /* already closed */
          }
        });
      },
    });
    return new Response(body, {
      status: 200,
      headers: { 'content-type': 'text/event-stream', ...response.headers },
    });
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
