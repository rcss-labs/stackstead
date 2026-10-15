import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installFakeStream, type FakeStream } from '../../vitest-setup';
import { createLogStream, lineTone, mergeCursor } from './logs';

const PATH = '/api/stacks/web/logs';
const C_A = 'aaaaaaaaaaaa';
const C_B = 'bbbbbbbbbbbb';
let fake: FakeStream;
const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

function logEvent(container: string, ns: number, ord: number, text = 'hi', service = 'api') {
  const data = JSON.stringify({ service, container, stream: 'stdout', time: '2026-10-14T10:00:00Z', text });
  return `id: ${container}:${ns}:${ord}\nevent: log\ndata: ${data}\n\n`;
}
const immediate = (cb: FrameRequestCallback) => {
  queueMicrotask(() => cb(0));
  return 0;
};

let streams: ReturnType<typeof createLogStream>[] = [];
function make(opts: Partial<Parameters<typeof createLogStream>[1]> = {}) {
  const s = createLogStream('web', { tail: 200, follow: true, ...opts }, { schedule: immediate });
  streams.push(s);
  return s;
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  streams.forEach((s) => s.close());
  streams = [];
  fake?.restore();
  vi.useRealTimers();
});

describe('mergeCursor', () => {
  it('keeps the latest id per container and ignores malformed ids', () => {
    const m = new Map<string, string>();
    mergeCursor(m, `${C_A}:10:1`);
    mergeCursor(m, `${C_A}:12:2`);
    mergeCursor(m, `${C_B}:11:1`);
    mergeCursor(m, 'garbage');
    mergeCursor(m, ':1:2');
    mergeCursor(m, `${C_A}:x:2`);
    expect([...m.entries()]).toEqual([
      [C_A, `${C_A}:12:2`],
      [C_B, `${C_B}:11:1`],
    ]);
  });
});

describe('createLogStream', () => {
  it('builds the query and buffers log events', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'one'), logEvent(C_A, 2, 1, 'two')] });
    const s = make({ service: 'api', tail: 'all' });
    await tick();
    expect(fake.requests[0].path).toBe(`${PATH}?service=api&tail=all&follow=1`);
    expect(s.buffer.snapshot().map((l) => l.text)).toEqual(['one', 'two']);
  });

  it('omits service and follow when unset', async () => {
    fake = installFakeStream({ [PATH]: [] });
    make({ follow: false });
    await tick();
    expect(fake.requests[0].path).toBe(`${PATH}?tail=200`);
  });

  it('TestResumeSendsCompositeCursor', async () => {
    fake = installFakeStream({
      [PATH]: { responses: [{ chunks: [logEvent(C_A, 10, 1), logEvent(C_B, 11, 1), logEvent(C_A, 12, 2)] }, { chunks: [] }] },
    });
    const s = make();
    await tick();
    fake.error(PATH, new TypeError('network'));
    await tick(2000);
    expect(fake.requests).toHaveLength(2);
    const sent = fake.requests[1].headers.get('Last-Event-ID') ?? '';
    expect(sent.split(',').sort()).toEqual([`${C_A}:12:2`, `${C_B}:11:1`]);
    expect(fake.requests[1].path).not.toContain('since');
    expect(s.cursors().split(',').sort()).toEqual([`${C_A}:12:2`, `${C_B}:11:1`]);
    expect(s.buffer.snapshot()).toHaveLength(3); // buffer kept across the reconnect
  });

  it('restart clears cursors, buffer and errors and uses the new query', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 10, 1)] });
    const s = make();
    await tick();
    expect(s.cursors()).not.toBe('');
    s.restart({ service: 'worker', tail: 100, follow: true });
    expect(s.cursors()).toBe('');
    expect(s.buffer.snapshot()).toHaveLength(0);
    await tick();
    expect(fake.requests).toHaveLength(2);
    expect(fake.requests[1].path).toBe(`${PATH}?service=worker&tail=100&follow=1`);
    expect(fake.requests[1].headers.get('Last-Event-ID')).toBeNull();
  });

  it('records per-container errors without ending the stream', async () => {
    const err = JSON.stringify({ service: 'api', container: C_A, error: 'no such container' });
    fake = installFakeStream({ [PATH]: [`event: error\ndata: ${err}\n\n`, logEvent(C_B, 1, 1, 'still here')] });
    const s = make();
    await tick();
    expect(get(s.errors)).toEqual([{ service: 'api', container: C_A, error: 'no such container' }]);
    expect(get(s.ended)).toBe(false);
    expect(s.buffer.snapshot()).toHaveLength(1);
  });

  it('end marks ended and stops reconnecting', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1), 'event: end\ndata: {}\n\n'] });
    const s = make({ follow: false });
    await tick();
    expect(get(s.ended)).toBe(true);
    await tick(5000);
    expect(fake.requests).toHaveLength(1);
  });

  it('counts coalescer-trimmed lines in dropped', async () => {
    const many = Array.from({ length: 30 }, (_, i) => logEvent(C_A, i + 1, 1, `l${i}`)).join('');
    fake = installFakeStream({ [PATH]: [many] });
    const s = createLogStream('web', { tail: 'all', follow: true }, { schedule: immediate, capacity: 10 });
    streams.push(s);
    await tick();
    expect(s.buffer.snapshot()).toHaveLength(10);
    expect(get(s.dropped)).toBe(20);
  });

  it('close stops the stream and ignores later events', async () => {
    fake = installFakeStream({ [PATH]: [] });
    const s = make();
    await tick();
    s.close();
    expect(get(s.status)).toBe('closed');
  });

  it('ignores late messages from the old handle after restart', async () => {
    const handlers: ((m: { event: string; data: string; id?: string }) => void)[] = [];
    const open = vi.fn((o: { onMessage: (m: { event: string; data: string; id?: string }) => void }) => {
      handlers.push(o.onMessage);
      return { close() {}, status: 'open', lastEventId: undefined };
    });
    const s = createLogStream('web', { tail: 200, follow: true }, { schedule: immediate, open: open as never });
    streams.push(s);
    const data = JSON.stringify({ service: 'api', container: C_A, stream: 'stdout', time: 't', text: 'late' });
    s.restart({ tail: 200, follow: true });
    handlers[0]({ event: 'log', data, id: `${C_A}:1:1` });
    await tick();
    expect(s.buffer.snapshot()).toHaveLength(0);
    expect(s.cursors()).toBe('');
    handlers[1]({ event: 'log', data, id: `${C_A}:2:1` });
    await tick();
    expect(s.buffer.snapshot()).toHaveLength(1);
  });

  it('strips ANSI escapes from log text', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, '\u001b[31mred\u001b[0m text')] });
    const s = make();
    await tick();
    expect(s.buffer.snapshot()[0].text).toBe('red text');
  });

  it('clear resets the buffer and the dropped count', async () => {
    const many = Array.from({ length: 30 }, (_, i) => logEvent(C_A, i + 1, 1, `l${i}`)).join('');
    fake = installFakeStream({ [PATH]: [many] });
    const s = createLogStream('web', { tail: 'all', follow: true }, { schedule: immediate, capacity: 10 });
    streams.push(s);
    await tick();
    s.clear();
    expect(s.buffer.snapshot()).toHaveLength(0);
    expect(get(s.dropped)).toBe(0);
  });
});

describe('lineTone', () => {
  it.each([
    ['2026 [Error] boom', 'danger'],
    ['ERROR: failed', 'danger'],
    ['[Warn] slow', 'warning'],
    ['WARN disk', 'warning'],
    ['all good', undefined],
    ['[Error] and WARN', 'danger'],
  ])('%s -> %s', (text, tone) => {
    expect(lineTone(text)).toBe(tone);
  });
});
