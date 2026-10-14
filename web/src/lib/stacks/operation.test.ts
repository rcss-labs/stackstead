import { afterEach, describe, expect, it } from 'vitest';
import { get } from 'svelte/store';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream } from '../../vitest-setup';
import { createOperationStream } from './operation';

const PATH = '/api/operations/op1/stream';
const line = (id: string | undefined, text: string) =>
  `${id === undefined ? '' : `id: ${id}\n`}event: line\ndata: ${JSON.stringify(text)}\n\n`;
const doneEv = (state = 'succeeded') =>
  `event: done\ndata: ${JSON.stringify({ id: 'op1', stack: 's', action: 'up', state, startedAt: '2026-01-01T00:00:00Z', exitCode: 0 })}\n\n`;
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
    expect(get(s.lines)).toEqual(['a', 'b', 'c']);
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
    expect(get(s.lines)).toEqual(['l0', 'l1', 'l2', 'l3', 'l4']);
    s.close();
  });

  it('shows the dropped note (no id) first and keeps it a single line', async () => {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: detail } });
    stream = installFakeStream({
      [PATH]: [line(undefined, '... 5 earlier lines dropped'), line('5', 'a'), doneEv()],
    });
    const s = createOperationStream('op1');
    await sleep();
    expect(get(s.lines)).toEqual(['... 5 earlier lines dropped', 'a']);
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
    expect(get(s.lines)).toEqual(['old']);
    s.close();
  });

  it('caps the client buffer at 20000 lines', async () => {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: detail } });
    const chunks: string[] = [];
    for (let i = 0; i < 20_050; i++) chunks.push(line(String(i), `l${i}`));
    stream = installFakeStream({ [PATH]: [chunks.join(''), doneEv()] });
    const s = createOperationStream('op1');
    await sleep(200);
    const lines = get(s.lines);
    expect(lines).toHaveLength(20_000);
    expect(lines[19_999]).toBe('l20049');
    s.close();
  });
});
