import { afterEach, describe, expect, it, vi } from 'vitest';
import { setupRestore } from './setupRestore';

afterEach(() => vi.unstubAllGlobals());

const originalStream = Blob.prototype.stream;
afterEach(() => {
  Blob.prototype.stream = originalStream;
});

/** jsdom's Blob has no stream(); give it one so the streaming path is taken. */
function enableBlobStream(): void {
  Blob.prototype.stream = function stream() {
    return new ReadableStream({ start: (c) => (c.enqueue(new TextEncoder().encode('archive')), c.close()) });
  };
}

describe('setupRestore.upload', () => {
  it('retries with the plain Blob and no progress when the streamed body is rejected', async () => {
    // Chrome rejects a streamed body over HTTP/1.1 (a proxy that downgrades) with a TypeError.
    vi.stubGlobal('window', { location: { protocol: 'https:' } });
    const bodies: unknown[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init: RequestInit & { duplex?: string }) => {
        bodies.push(init.body);
        if (init.duplex === 'half') throw new TypeError('Failed to fetch');
        return new Response(JSON.stringify({ source: { kind: 'upload' } }), { status: 200 });
      }),
    );
    enableBlobStream();
    const file = new Blob(['archive']);
    const progress: number[] = [];

    await setupRestore.upload(file, 'token', (n) => progress.push(n));

    expect(bodies).toHaveLength(2);
    expect(bodies[0]).not.toBe(file);
    expect(bodies[1]).toBe(file);
  });

  it('does not retry on an HTTP error', async () => {
    vi.stubGlobal('window', { location: { protocol: 'https:' } });
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ error: 'nope' }), { status: 403 }));
    vi.stubGlobal('fetch', fetchMock);
    await expect(setupRestore.upload(new Blob(['x']), 'token', () => {})).rejects.toMatchObject({ status: 403 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
