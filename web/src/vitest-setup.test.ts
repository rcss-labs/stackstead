import { afterEach, describe, expect, it } from 'vitest';
import { installFakeFetch, installFakeStream } from './vitest-setup';

describe('installFakeFetch', () => {
  let restore: (() => void) | undefined;
  afterEach(() => restore?.());

  it('serves JSON for a METHOD path route and records the call', async () => {
    const fake = installFakeFetch({ 'GET /api/me': { body: { username: 'admin' } } });
    restore = fake.restore;
    const res = await fetch('/api/me');
    expect(res.status).toBe(200);
    expect(res.headers.get('content-type')).toContain('application/json');
    expect(await res.json()).toEqual({ username: 'admin' });
    expect(fake.calls).toHaveLength(1);
    expect(fake.calls[0]).toMatchObject({ method: 'GET', path: '/api/me' });
  });

  it('passes the parsed body and headers to handlers and honours status', async () => {
    const fake = installFakeFetch({
      'POST /api/login': (call) => ({ status: 401, body: { error: `no ${(call.body as { username: string }).username}` } }),
    });
    restore = fake.restore;
    const res = await fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: 'bob' }),
    });
    expect(res.status).toBe(401);
    expect(await res.json()).toEqual({ error: 'no bob' });
    expect(fake.calls[0]?.headers.get('content-type')).toBe('application/json');
  });

  it('answers 404 for unrouted requests instead of touching the network', async () => {
    const fake = installFakeFetch({});
    restore = fake.restore;
    const res = await fetch('/api/nope');
    expect(res.status).toBe(404);
    expect(fake.calls).toHaveLength(1);
  });

  it('restores the original fetch', () => {
    const original = globalThis.fetch;
    const fake = installFakeFetch({});
    expect(globalThis.fetch).not.toBe(original);
    fake.restore();
    expect(globalThis.fetch).toBe(original);
  });
});

describe('installFakeStream', () => {
  let restore: (() => void) | undefined;
  afterEach(() => restore?.());

  it('streams initial chunks, then pushed chunks, then closes', async () => {
    const fake = installFakeStream({ '/api/events': ['a: 1\n\n'] });
    restore = fake.restore;
    const res = await fetch('/api/events');
    expect(res.headers.get('content-type')).toContain('text/event-stream');
    const reader = res.body!.getReader();
    const dec = new TextDecoder();
    expect(dec.decode((await reader.read()).value)).toBe('a: 1\n\n');
    fake.push('/api/events', ': ping\n\n');
    expect(dec.decode((await reader.read()).value)).toBe(': ping\n\n');
    fake.close('/api/events');
    expect((await reader.read()).done).toBe(true);
  });

  it('records requests including headers such as Last-Event-ID', async () => {
    const fake = installFakeStream({ '/api/events': [] });
    restore = fake.restore;
    await fetch('/api/events', { headers: { 'Last-Event-ID': '7' } });
    expect(fake.requests).toHaveLength(1);
    expect(fake.requests[0]?.headers.get('last-event-id')).toBe('7');
  });

  it('errors the stream when asked', async () => {
    const fake = installFakeStream({ '/s': [] });
    restore = fake.restore;
    const reader = (await fetch('/s')).body!.getReader();
    fake.error('/s', new Error('boom'));
    await expect(reader.read()).rejects.toThrow('boom');
  });
});
