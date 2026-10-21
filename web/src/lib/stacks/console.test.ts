import { describe, expect, it, vi } from 'vitest';
import type { LogLine } from '../logs/buffer';
import {
  clampLogsHeight,
  consoleStatus,
  filterConsoleLines,
  levelMatches,
  LOGS_HEIGHT_KEY,
  logClock,
  logUtc,
  newLinesSince,
  readLogsHeight,
  searchSegments,
  writeLogsHeight,
} from './console';

function line(seq: number, text: string): LogLine {
  return { seq, text, service: 'web', container: 'c1', stream: 'stdout', ts: '2026-01-01T00:00:00Z' } as unknown as LogLine;
}

describe('console derivations', () => {
  it('levels match lineTone', () => {
    expect(['all', 'warn', 'error'].map((l) => levelMatches('ERROR x', l as never))).toEqual([true, true, true]);
    expect(['all', 'warn', 'error'].map((l) => levelMatches('WARN y', l as never))).toEqual([true, true, false]);
    expect(['all', 'warn', 'error'].map((l) => levelMatches('plain', l as never))).toEqual([true, false, false]);
  });

  it('filterConsoleLines combines search and level', () => {
    const lines = [line(1, 'WARN boom'), line(2, 'ERROR boom'), line(3, 'WARN other'), line(4, 'boom')];
    expect(filterConsoleLines(lines, { level: 'warn', search: 'boom' }).map((l) => l.seq)).toEqual([1, 2]);
    expect(filterConsoleLines(lines, { level: 'all' })).toHaveLength(4);
  });

  it('newLinesSince counts past the frozen seq and never goes negative', () => {
    expect(newLinesSince([line(5, 'a'), line(9, 'b')], 5)).toBe(4);
    expect(newLinesSince([line(3, 'a')], 5)).toBe(0);
    expect(newLinesSince([], 5)).toBe(0);
  });

  it('searchSegments splits case-insensitively and skips when lowercasing changes length', () => {
    expect(searchSegments('a BOOM b', 'boom')).toEqual([
      { text: 'a ', match: false },
      { text: 'BOOM', match: true },
      { text: ' b', match: false },
    ]);
    expect(searchSegments('a BOOM b', 'Boom')).toEqual([
      { text: 'a ', match: false },
      { text: 'BOOM', match: true },
      { text: ' b', match: false },
    ]);
    expect(searchSegments('İ', 'i')).toEqual([{ text: 'İ', match: false }]);
    expect(searchSegments('abc', '')).toEqual([{ text: 'abc', match: false }]);
  });

  it('clampLogsHeight clamps and defaults', () => {
    expect(clampLogsHeight('300')).toBe(300);
    expect(clampLogsHeight(999)).toBe(560);
    expect(clampLogsHeight(10)).toBe(120);
    expect(clampLogsHeight('x')).toBe(240);
    expect(clampLogsHeight(undefined)).toBe(240);
  });

  it('read and write use the key and survive a throwing storage', () => {
    const getItem = vi.fn(() => '300');
    const setItem = vi.fn();
    expect(readLogsHeight({ getItem })).toBe(300);
    expect(getItem).toHaveBeenCalledWith(LOGS_HEIGHT_KEY);
    writeLogsHeight({ setItem }, 300);
    expect(setItem).toHaveBeenCalledWith('stackstead.logsHeight', '300');
    expect(
      readLogsHeight({
        getItem: () => {
          throw new Error('denied');
        },
      }),
    ).toBeUndefined();
    expect(readLogsHeight(undefined)).toBeUndefined();
    expect(readLogsHeight({ getItem: () => '1200' })).toBe(1200);
    expect(readLogsHeight({ getItem: () => 'invalid' })).toBeUndefined();
    expect(() =>
      writeLogsHeight(
        {
          setItem: () => {
            throw new Error('denied');
          },
        },
        300,
      ),
    ).not.toThrow();
  });

  it('formats clock and utc, falling back to the raw input', () => {
    expect(logClock('nope')).toBe('nope');
    expect(logUtc('nope')).toBe('nope');
    expect(logUtc('2026-01-01T00:00:00Z')).toBe('2026-01-01T00:00:00.000Z');
  });

  it('formats the clock as zero-padded 24-hour local time whatever the locale', () => {
    expect(logClock(new Date(2026, 9, 14, 21, 57, 35).toISOString())).toBe('21:57:35');
    expect(logClock(new Date(2026, 9, 14, 5, 41, 51).toISOString())).toBe('05:41:51');
    expect(logClock(new Date(2026, 9, 14, 0, 0, 0).toISOString())).toBe('00:00:00');
  });

  it('status labels and tones', () => {
    const base = { ended: false, paused: false, sourceKind: 'stack' as const };
    expect(consoleStatus({ ...base, status: 'open' })).toEqual({ label: 'live', tone: 'live' });
    expect(consoleStatus({ ...base, status: 'open', paused: true })).toEqual({ label: 'paused', tone: 'warning' });
    // A follow stream stays open while its container is stopped: say so instead of live.
    expect(consoleStatus({ ...base, status: 'open', containerState: 'exited' })).toEqual({
      label: 'waiting - container exited',
      tone: 'warning',
    });
    expect(consoleStatus({ ...base, status: 'open', containerState: 'running' })).toEqual({ label: 'live', tone: 'live' });
    expect(consoleStatus({ ...base, status: 'reconnecting' })).toEqual({ label: 'reconnecting', tone: 'warning' });
    expect(consoleStatus({ ...base, status: 'gone', sourceKind: 'container' })).toEqual({
      label: 'container or logs no longer available',
      tone: 'muted',
    });
    expect(consoleStatus({ ...base, status: 'unauthorized' })).toEqual({ label: 'session expired - sign in again', tone: 'muted' });
    expect(consoleStatus({ ...base, status: 'open', ended: true })).toEqual({ label: 'ended', tone: 'muted' });
  });
});
