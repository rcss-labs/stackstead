import { describe, expect, it, vi } from 'vitest';
import type { LogEvent } from '../api/types';
import { createCoalescer, createLogBuffer, filterLines } from './buffer';

function ev(i: number, over: Partial<LogEvent> = {}): LogEvent {
  return { service: 'web', container: 'c1', stream: 'stdout', time: `t${i}`, text: `line ${i}`, ...over };
}
function events(from: number, n: number): LogEvent[] {
  return Array.from({ length: n }, (_, i) => ev(from + i));
}

describe('createLogBuffer', () => {
  it('TestRingBufferCapsAndCountsDropped', () => {
    const buf = createLogBuffer(5000);
    buf.push(events(0, 12000));
    const snap = buf.snapshot();
    expect(snap).toHaveLength(5000);
    expect(snap[0].text).toBe('line 7000');
    expect(snap[snap.length - 1].text).toBe('line 11999');
    expect(buf.dropped()).toBe(7000);
  });

  it('keeps order and strictly increasing seq across wraparound', () => {
    const buf = createLogBuffer(3);
    buf.push(events(0, 2));
    buf.push(events(2, 2));
    const snap = buf.snapshot();
    expect(snap.map((l) => l.text)).toEqual(['line 1', 'line 2', 'line 3']);
    for (let i = 1; i < snap.length; i++) expect(snap[i].seq).toBeGreaterThan(snap[i - 1].seq);
    expect(buf.dropped()).toBe(1);
  });

  it('pushes 10 000 lines quickly', () => {
    const buf = createLogBuffer();
    const lines = events(0, 10000);
    const start = performance.now();
    buf.push(lines);
    expect(performance.now() - start).toBeLessThan(50);
  });

  it('notifies subscribers once per push and on clear', () => {
    const buf = createLogBuffer(10);
    const seen: number[] = [];
    const unsub = buf.subscribe((l) => seen.push(l.length));
    expect(seen).toEqual([0]);
    buf.push(events(0, 4));
    expect(seen).toEqual([0, 4]);
    buf.clear();
    expect(seen).toEqual([0, 4, 0]);
    unsub();
  });

  it('clear resets lines and dropped but seq keeps increasing', () => {
    const buf = createLogBuffer(2);
    buf.push(events(0, 5));
    const lastSeq = buf.snapshot()[1].seq;
    buf.clear();
    expect(buf.snapshot()).toEqual([]);
    expect(buf.dropped()).toBe(0);
    buf.push(events(5, 1));
    expect(buf.snapshot()[0].seq).toBeGreaterThan(lastSeq);
  });

  it('ignores an empty push without notifying', () => {
    const buf = createLogBuffer(2);
    const fn = vi.fn();
    buf.subscribe(fn);
    buf.push([]);
    expect(fn).toHaveBeenCalledTimes(1);
  });
});

describe('filterLines', () => {
  const buf = createLogBuffer();
  buf.push([
    ev(1, { service: 'web', stream: 'stdout', text: 'Hello World' }),
    ev(2, { service: 'web', stream: 'stderr', text: 'boom: hello' }),
    ev(3, { service: 'db', stream: 'stdout', text: 'ready (a.b)' }),
  ]);
  const lines = buf.snapshot();

  it('returns everything for an empty filter', () => {
    expect(filterLines(lines, {})).toHaveLength(3);
  });
  it('filters by service and stream', () => {
    expect(filterLines(lines, { service: 'web' })).toHaveLength(2);
    expect(filterLines(lines, { service: 'web', stream: 'stderr' }).map((l) => l.text)).toEqual(['boom: hello']);
  });
  it('searches case-insensitively as a plain substring, not a regex', () => {
    expect(filterLines(lines, { search: 'HELLO' })).toHaveLength(2);
    expect(filterLines(lines, { search: 'a.b' })).toHaveLength(1);
    expect(filterLines(lines, { search: 'a.' })).toHaveLength(1);
    expect(filterLines(lines, { search: '.*' })).toHaveLength(0);
  });
  it('combines service, stream and search', () => {
    expect(filterLines(lines, { service: 'web', stream: 'stdout', search: 'hello' })).toHaveLength(1);
    expect(filterLines(lines, { service: 'db', search: 'hello' })).toHaveLength(0);
  });
});

describe('createCoalescer', () => {
  function fakeScheduler() {
    const queue: FrameRequestCallback[] = [];
    const schedule = vi.fn((cb: FrameRequestCallback) => {
      queue.push(cb);
      return queue.length;
    });
    const frame = () => queue.splice(0).forEach((cb) => cb(0));
    return { schedule, frame, pending: () => queue.length };
  }

  it('TestCoalesceFlushesOncePerFrame', () => {
    const s = fakeScheduler();
    const flush = vi.fn();
    const c = createCoalescer<number>(flush, s.schedule);
    c.add(1);
    c.add([2]);
    c.add(3);
    expect(flush).not.toHaveBeenCalled();
    expect(s.schedule).toHaveBeenCalledTimes(1);
    s.frame();
    expect(flush).toHaveBeenCalledTimes(1);
    expect(flush).toHaveBeenCalledWith([1, 2, 3]);
    s.frame();
    expect(flush).toHaveBeenCalledTimes(1);
  });

  it('an add during flush schedules the next frame', () => {
    const s = fakeScheduler();
    const batches: number[][] = [];
    const c = createCoalescer<number>((items) => {
      batches.push(items);
      if (items[0] === 1) c.add(2);
    }, s.schedule);
    c.add(1);
    s.frame();
    expect(s.pending()).toBe(1);
    s.frame();
    expect(batches).toEqual([[1], [2]]);
  });

  it('cancel drops pending items and a stale frame does nothing', () => {
    const s = fakeScheduler();
    const flush = vi.fn();
    const c = createCoalescer<number>(flush, s.schedule);
    c.add([1, 2]);
    c.cancel();
    s.frame();
    expect(flush).not.toHaveBeenCalled();
    c.add(3);
    s.frame();
    expect(flush).toHaveBeenCalledWith([3]);
  });
});
