import { afterEach, describe, expect, it } from 'vitest';
import { get } from '$lib/store';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream } from '../../vitest-setup';
import { createOperationStream } from './operation';

const PATH = '/api/operations/op1/stream';
const line = (id: string | undefined, text: string) =>
  `${id === undefined ? '' : `id: ${id}\n`}event: line\ndata: ${JSON.stringify(text)}\n\n`;
const doneEv = (state = 'succeeded') =>
  `event: done\ndata: ${JSON.stringify({ id: 'op1', stack: 's', action: 'up', state, startedAt: '2026-01-01T00:00:00Z', exitCode: 0 })}\n\n`;
const texts = (s: ReturnType<typeof createOperationStream>) => get(s.lines).map((e) => e.text);
const sleep = (ms = 20) => new Promise((r) => setTimeout(r, ms));

let stream: FakeStream;
let fetcher: FakeFetch;
afterEach(() => {
  stream?.restore();
  fetcher?.restore();
});

const detail = { id: 'op1', stack: 's', action: 'up', state: 'running', startedAt: '2026-01-01T00:00:00Z', lines: ['x'] };

describe('createOperationStream', () => {
  it('TestResumeSendsLastEventId: reconnect after dropped sends the last id', async () => {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: detail } });
    stream = installFakeStream({
      [PATH]: { responses: [{ chunks: [line('0', 'a'), line('1', 'b'), 'event: dropped\ndata: {}\n\n'] }, { chunks: [line('2', 'c'), doneEv()] }] },
    });
    const s = createOperationStream('op1');
    await sleep();
    expect(stream.requests).toHaveLength(2);
    expect(stream.requests[0].headers.has('Last-Event-ID')).toBe(false);
    expect(stream.requests[1].headers.get('Last-Event-ID')).toBe('1');
    expect(texts(s)).toEqual(['a', 'b', 'c']);
    s.close();
  });

  it('TestNoDuplicateLinesAcrossReconnect: replayed lines are not shown twice', async () => {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: detail } });
    stream = installFakeStream({
      [PATH]: {
        responses: [
          { chunks: [line('0', 'l0'), line('1', 'l1'), line('2', 'l2'), 'event: dropped\ndata: {}\n\n'] },
          // A server that replays from 2 anyway (the contract says after 2, but be robust).
          { chunks: [line('2', 'l2'), line('3', 'l3'), line('4', 'l4'), doneEv()] },
        ],
      },
    });
    const s = createOperationStream('op1');
    await sleep();
    expect(texts(s)).toEqual(['l0', 'l1', 'l2', 'l3', 'l4']);
    s.close();
  });

  it('shows the dropped note (no id) first and keeps it a single line', async () => {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: detail } });
    stream = installFakeStream({
      [PATH]: [line(undefined, '... 5 earlier lines dropped'), line('5', 'a'), doneEv()],
    });
    const s = createOperationStream('op1');
    await sleep();
    expect(texts(s)).toEqual(['... 5 earlier lines dropped', 'a']);
    expect(stream.requests).toHaveLength(1);
    s.close();
  });

  it('done sets the final operation and ends the stream', async () => {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: detail } });
    stream = installFakeStream({ [PATH]: [line('0', 'a'), doneEv('failed')] });
    const s = createOperationStream('op1');
    await sleep();
    expect(get(s.op)?.state).toBe('failed');
    expect(get(s.status)).toBe('closed');
    await sleep(50);
    expect(stream.requests).toHaveLength(1);
  });

  it('fetches the operation once when the stream is gone without done', async () => {
    fetcher = installFakeFetch({
      'GET /api/operations/op1': { body: { ...detail, state: 'failed', lines: ['old'] } },
    });
    stream = installFakeStream({ [PATH]: { responses: [{ status: 404, body: { error: 'nope' } }] } });
    const s = createOperationStream('op1');
    await sleep();
    expect(get(s.op)?.state).toBe('failed');
    expect(texts(s)).toEqual(['old']);
    s.close();
  });

  it('caps the client buffer (trimming in chunks) and notes the trim', async () => {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: detail } });
    const chunks: string[] = [];
    for (let i = 0; i < 22_000; i++) chunks.push(line(String(i), `l${i}`));
    stream = installFakeStream({ [PATH]: [chunks.join(''), doneEv()] });
    const s = createOperationStream('op1');
    await sleep(200);
    const entries = get(s.lines);
    const n = entries.filter((e) => e.kind === 'line').length;
    expect(n).toBeGreaterThanOrEqual(20_000);
    expect(n).toBeLessThanOrEqual(21_000);
    expect(entries[entries.length - 1].text).toBe('l21999');
    expect(entries[0]).toMatchObject({ kind: 'note' });
    expect(entries[0].text).toMatch(/earlier lines dropped/);
    s.close();
  });

  it('marks the gap in place when a resume skips evicted lines, with no top note', async () => {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: detail } });
    stream = installFakeStream({
      [PATH]: {
        responses: [
          { chunks: [line('0', 'a'), line('1', 'b'), 'event: dropped\ndata: {}\n\n'] },
          // Server evicted 2..9: it replays everything it has, led by the note.
          { chunks: [line(undefined, '... 10 earlier lines dropped'), line('10', 'k'), doneEv()] },
        ],
      },
    });
    const s = createOperationStream('op1');
    await sleep();
    expect(texts(s)).toEqual(['a', 'b', '… 8 lines missed', 'k']);
    expect(get(s.lines).map((e) => e.kind)).toEqual(['line', 'line', 'gap', 'line']);
    s.close();
  });

  it('publishes a bounded number of times for a 20k-line replay', async () => {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: detail } });
    const chunks: string[] = [];
    for (let i = 0; i < 20_000; i++) chunks.push(line(String(i), `l${i}`));
    stream = installFakeStream({ [PATH]: [chunks.join(''), doneEv()] });
    const s = createOperationStream('op1');
    let publishes = 0;
    s.lines.subscribe(() => publishes++);
    await sleep(300);
    expect(publishes).toBeLessThan(20);
    expect(get(s.lines)).toHaveLength(20_000);
    s.close();
  });

  it('has stable unique keys per entry', async () => {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: detail } });
    stream = installFakeStream({ [PATH]: [line('0', 'same'), line('1', 'same'), doneEv()] });
    const s = createOperationStream('op1');
    await sleep();
    const keys = get(s.lines).map((e) => e.key);
    expect(new Set(keys).size).toBe(2);
    s.close();
  });

  it('reports unavailable when the stream is gone and the operation cannot be fetched', async () => {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { status: 404, body: { error: 'nope' } } });
    stream = installFakeStream({ [PATH]: { responses: [{ status: 404, body: { error: 'nope' } }] } });
    const s = createOperationStream('op1');
    await sleep();
    expect(get(s.unavailable)).toBe(true);
    expect(get(s.op)).toBeUndefined();
    s.close();
  });
});
