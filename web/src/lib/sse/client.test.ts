import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installFakeStream, type FakeStream } from '../../vitest-setup';
import { openSSE, type SSEMessage, type SSEStatus } from './client';

let fake: FakeStream;
beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  fake?.restore();
  vi.useRealTimers();
  vi.restoreAllMocks();
});

const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

function collect() {
  const messages: SSEMessage[] = [];
  const statuses: SSEStatus[] = [];
  const infos: Array<{ attempt: number; error?: string } | undefined> = [];
  return {
    messages,
    statuses,
    infos,
    onMessage: (m: SSEMessage) => messages.push(m),
    onStatus: (s: SSEStatus, i?: { attempt: number; error?: string }) => {
      statuses.push(s);
      infos.push(i);
    },
  };
}

describe('openSSE', () => {
  it('delivers messages, sends the SSE headers and reports open', async () => {
    fake = installFakeStream({ '/s': ['id: 1\nevent: e\ndata: hi\n\n'] });
    const c = collect();
    const h = openSSE({ url: '/s', ...c });
    await tick();
    expect(c.messages).toEqual([{ event: 'e', data: 'hi', id: '1' }]);
    expect(c.statuses).toEqual(['connecting', 'open']);
    expect(h.status).toBe('open');
    expect(fake.requests[0].headers.get('Accept')).toBe('text/event-stream');
    expect(fake.requests[0].headers.has('Last-Event-ID')).toBe(false);
    h.close();
  });

  it('TestReconnectSendsLastEventId', async () => {
    fake = installFakeStream({
      '/s': { responses: [{ chunks: ['id: 7\ndata: a\n\n'] }, { chunks: [] }] },
    });
    const c = collect();
    const h = openSSE({ url: '/s', ...c });
    await tick();
    fake.error('/s', new TypeError('network'));
    await tick(2000);
    expect(fake.requests).toHaveLength(2);
    expect(fake.requests[1].headers.get('Last-Event-ID')).toBe('7');
    expect(h.lastEventId).toBe('7');
    expect(c.statuses).toContain('reconnecting');
    h.close();
  });

  it('keeps the last id when later messages carry none', async () => {
    fake = installFakeStream({
      '/s': { responses: [{ chunks: ['id: 7\ndata: a\n\ndata: dropped note\n\n'] }, { chunks: [] }] },
    });
    const h = openSSE({ url: '/s', onMessage() {} });
    await tick();
    fake.close('/s');
    await tick(2000);
    expect(fake.requests[1].headers.get('Last-Event-ID')).toBe('7');
    h.close();
  });

  it('TestLastEventIdProviderWins', async () => {
    fake = installFakeStream({
      '/s': { responses: [{ chunks: ['id: 7\ndata: a\n\n'] }, { chunks: [] }, { chunks: [] }] },
    });
    let answer: string | undefined = 'a:1:1,b:2:1';
    const h = openSSE({ url: '/s', onMessage() {}, lastEventId: () => answer });
    await tick();
    expect(fake.requests[0].headers.get('Last-Event-ID')).toBe('a:1:1,b:2:1');
    fake.close('/s');
    await tick(2000);
    expect(fake.requests[1].headers.get('Last-Event-ID')).toBe('a:1:1,b:2:1');
    answer = undefined;
    fake.close('/s');
    await tick(5000);
    expect(fake.requests[2].headers.has('Last-Event-ID')).toBe(false);
    h.close();
  });

  it('uses a string lastEventId as the initial id', async () => {
    fake = installFakeStream({ '/s': [] });
    const h = openSSE({ url: '/s', onMessage() {}, lastEventId: '41' });
    await tick();
    expect(fake.requests[0].headers.get('Last-Event-ID')).toBe('41');
    h.close();
  });

  it('TestHeartbeatTimeoutReconnects', async () => {
    fake = installFakeStream({ '/s': { responses: [{ chunks: [] }, { chunks: [] }] } });
    const c = collect();
    const h = openSSE({ url: '/s', ...c });
    await tick();
    await tick(44_000);
    expect(fake.requests).toHaveLength(1);
    await tick(1_000); // 45 s of silence: abort, then backoff
    expect(fake.requests[0].signal?.aborted).toBe(true);
    await tick(2_000);
    expect(fake.requests).toHaveLength(2);
    expect(c.statuses).toContain('reconnecting');
    h.close();
  });

  it('a comment resets the heartbeat watchdog', async () => {
    fake = installFakeStream({ '/s': { responses: [{ chunks: [] }, { chunks: [] }] } });
    const h = openSSE({ url: '/s', onMessage() {} });
    await tick();
    await tick(30_000);
    fake.push('/s', ': ping\n\n');
    await tick(30_000); // 60 s total but only 30 s since the ping
    expect(fake.requests).toHaveLength(1);
    expect(fake.requests[0].signal?.aborted).toBe(false);
    await tick(15_000);
    expect(fake.requests[0].signal?.aborted).toBe(true);
    h.close();
  });

  it('TestBackoffIsCappedAndJittered', async () => {
    fake = installFakeStream({ '/s': { responses: Array.from({ length: 12 }, () => ({ status: 503, body: { error: 'x' } })) } });
    const delays: number[] = [];
    const rnd = vi.spyOn(Math, 'random');
    rnd.mockReturnValue(0); // -20 % on the first wait, flipped below
    let call = 0;
    const h = openSSE({
      url: '/s',
      onMessage() {},
      backoff: { minMs: 1000, maxMs: 30000 },
      heartbeatTimeoutMs: 10 * 60_000,
      // Record only the reconnect waits, not the heartbeat watchdog.
      setTimeoutImpl: (fn, ms) => {
        if (ms !== 10 * 60_000) {
          delays.push(ms);
          rnd.mockReturnValue(++call % 2); // alternate -20 % / +20 %
        }
        return globalThis.setTimeout(fn, ms);
      },
    });
    await tick(10 * 60_000);
    const base = [1000, 2000, 4000, 8000, 16000, 30000, 30000, 30000];
    expect(delays.length).toBeGreaterThanOrEqual(base.length);
    base.forEach((b, i) => {
      expect(delays[i]).toBeCloseTo(b * (i % 2 === 0 ? 0.8 : 1.2), 5);
    });
    h.close();
  });

  it('a server retry: field overrides the minimum backoff', async () => {
    fake = installFakeStream({
      '/s': { responses: [{ chunks: ['retry: 5000\ndata: a\n\n'] }, { chunks: [] }] },
    });
    vi.spyOn(Math, 'random').mockReturnValue(0.5);
    const h = openSSE({ url: '/s', onMessage() {} });
    await tick();
    fake.close('/s');
    await tick(4_000);
    expect(fake.requests).toHaveLength(1);
    await tick(1_500);
    expect(fake.requests).toHaveLength(2);
    h.close();
  });

  it('TestStopsOn401', async () => {
    fake = installFakeStream({ '/s': { responses: [{ status: 401, body: { error: 'no' } }, { chunks: [] }] } });
    const c = collect();
    const h = openSSE({ url: '/s', ...c });
    await tick(60_000);
    expect(fake.requests).toHaveLength(1);
    expect(h.status).toBe('unauthorized');
    expect(c.statuses).toEqual(['connecting', 'unauthorized']);
  });

  it.each([403, 404])('%i means gone and is not retried', async (status) => {
    fake = installFakeStream({ '/s': { responses: [{ status, body: { error: 'no' } }, { chunks: [] }] } });
    const c = collect();
    const h = openSSE({ url: '/s', ...c });
    await tick(60_000);
    expect(fake.requests).toHaveLength(1);
    expect(h.status).toBe('gone');
    expect(c.statuses.at(-1)).toBe('gone');
  });

  it('retries other non-2xx answers', async () => {
    fake = installFakeStream({ '/s': { responses: [{ status: 502, body: { error: 'bad gateway' } }, { chunks: ['data: ok\n\n'] }] } });
    const c = collect();
    const h = openSSE({ url: '/s', ...c });
    await tick(5_000);
    expect(fake.requests).toHaveLength(2);
    expect(c.messages).toEqual([{ event: 'message', data: 'ok' }]);
    expect(c.statuses).toEqual(['connecting', 'reconnecting', 'open']);
    expect(c.infos[1]?.error).toContain('502');
    h.close();
  });

  it('TestCloseStopsEverything', async () => {
    fake = installFakeStream({ '/s': { responses: [{ chunks: ['data: a\n\n'] }, { chunks: [] }] } });
    const c = collect();
    const h = openSSE({ url: '/s', ...c });
    await tick();
    const seenMessages = c.messages.length;
    const seenStatuses = c.statuses.length;
    h.close();
    expect(h.status).toBe('closed');
    expect(fake.requests[0].signal?.aborted).toBe(true);
    await tick(120_000);
    expect(fake.requests).toHaveLength(1);
    expect(c.messages).toHaveLength(seenMessages);
    expect(c.statuses).toHaveLength(seenStatuses);
  });

  it('close() during backoff cancels the pending reconnect', async () => {
    fake = installFakeStream({ '/s': { responses: [{ status: 500, body: {} }, { chunks: [] }] } });
    const h = openSSE({ url: '/s', onMessage() {} });
    await tick();
    h.close();
    await tick(60_000);
    expect(fake.requests).toHaveLength(1);
  });

  it('a throwing onMessage is reported, keeps the stream healthy and never opens a second connection', async () => {
    fake = installFakeStream({ '/s': { responses: [{ chunks: ['data: a\n\n'] }, { chunks: [] }] } });
    const err = vi.spyOn(console, 'error').mockImplementation(() => {});
    const got: string[] = [];
    const h = openSSE({
      url: '/s',
      onMessage: (m) => {
        got.push(m.data);
        if (m.data === 'a') throw new Error('handler bug');
      },
    });
    await tick();
    fake.push('/s', 'data: b\n\n');
    await tick(60_000 * 0 + 1000);
    expect(got).toEqual(['a', 'b']);
    expect(err).toHaveBeenCalled();
    expect(fake.requests).toHaveLength(1);
    expect(fake.requests[0].signal?.aborted).toBe(false);
    h.close();
  });

  it('aborts the connection on every exit from the read phase', async () => {
    fake = installFakeStream({ '/s': { responses: [{ chunks: ['data: a\n\n'] }, { chunks: [] }] } });
    const h = openSSE({ url: '/s', onMessage() {} });
    await tick();
    fake.close('/s');
    await tick(2000);
    expect(fake.requests[0].signal?.aborted).toBe(true);
    expect(fake.requests).toHaveLength(2);
    h.close();
  });

  it('clamps a tiny server retry: value', async () => {
    fake = installFakeStream({ '/s': { responses: [{ chunks: ['retry: 1\ndata: a\n\n'] }, { chunks: [] }] } });
    vi.spyOn(Math, 'random').mockReturnValue(0.5);
    const h = openSSE({ url: '/s', onMessage() {} });
    await tick();
    fake.close('/s');
    await tick(100);
    expect(fake.requests).toHaveLength(1);
    await tick(300);
    expect(fake.requests).toHaveLength(2);
    h.close();
  });

  it('treats a 200 that is not text/event-stream as an error: no open, backoff continues', async () => {
    fake = installFakeStream({
      '/s': { responses: [{ status: 500, body: {} }, { headers: {}, chunks: [] }] },
    });
    const orig = globalThis.fetch;
    let n = 0;
    globalThis.fetch = (async () => {
      n++;
      return new Response('<html></html>', { status: 200, headers: { 'content-type': 'text/html' } });
    }) as typeof fetch;
    const c = collect();
    const h = openSSE({ url: '/s', ...c });
    await tick(10_000);
    expect(c.statuses).not.toContain('open');
    expect(c.statuses).toContain('reconnecting');
    expect(c.infos.at(-1)?.error).toContain('content-type');
    expect(c.infos.at(-1)?.attempt).toBeGreaterThan(0);
    expect(n).toBeGreaterThan(1);
    globalThis.fetch = orig;
    h.close();
  });

  it('decodes a multibyte character split across chunks', async () => {
    fake = installFakeStream({ '/s': [] });
    const c = collect();
    const h = openSSE({ url: '/s', ...c });
    await tick();
    const bytes = new TextEncoder().encode('data: h\u00e9llo\u20ac\n\n');
    const cut = bytes.indexOf(0xc3) + 1; // middle of "é"
    const cut2 = bytes.indexOf(0xe2) + 1; // middle of "€"
    const raw = (b: Uint8Array) => String.fromCharCode(...b);
    void raw;
    // push() takes text, so feed through a hand-built response instead.
    h.close();
    const orig = globalThis.fetch;
    let ctrl!: ReadableStreamDefaultController<Uint8Array>;
    globalThis.fetch = (async () =>
      new Response(new ReadableStream<Uint8Array>({ start: (cc) => (ctrl = cc) }), {
        status: 200,
        headers: { 'content-type': 'text/event-stream' },
      })) as typeof fetch;
    const c2 = collect();
    const h2 = openSSE({ url: '/x', ...c2 });
    await tick();
    ctrl.enqueue(bytes.slice(0, cut));
    ctrl.enqueue(bytes.slice(cut, cut2));
    ctrl.enqueue(bytes.slice(cut2));
    await tick();
    expect(c2.messages).toEqual([{ event: 'message', data: 'h\u00e9llo\u20ac' }]);
    globalThis.fetch = orig;
    h2.close();
  });

  it('an empty id: clears the Last-Event-ID header on reconnect', async () => {
    fake = installFakeStream({ '/s': { responses: [{ chunks: ['id: 5\ndata: a\n\nid:\ndata: b\n\n'] }, { chunks: [] }] } });
    const h = openSSE({ url: '/s', onMessage() {} });
    await tick();
    fake.close('/s');
    await tick(2000);
    expect(fake.requests[1].headers.has('Last-Event-ID')).toBe(false);
    h.close();
  });

  it('ignores an id containing NUL', async () => {
    fake = installFakeStream({ '/s': { responses: [{ chunks: ['id: 5\ndata: a\n\nid: 6\u0000x\ndata: b\n\n'] }, { chunks: [] }] } });
    const h = openSSE({ url: '/s', onMessage() {} });
    await tick();
    fake.close('/s');
    await tick(2000);
    expect(fake.requests[1].headers.get('Last-Event-ID')).toBe('5');
    h.close();
  });
});
