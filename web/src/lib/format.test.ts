import { describe, expect, it } from 'vitest';
import { formatBytes, formatDuration, formatPercent, formatRate, relativeTime } from './format';

describe('formatBytes', () => {
  it.each([
    [0, '0 B'],
    [1, '1 B'],
    [1023, '1023 B'],
    [1024, '1.0 KiB'],
    [1536, '1.5 KiB'],
    [1024 ** 2, '1.0 MiB'],
    [1.5 * 1024 ** 3, '1.5 GiB'],
    [1024 ** 4, '1.0 TiB'],
    [-5, '0 B'],
    [Number.NaN, '0 B'],
  ])('%s -> %s', (n, want) => {
    expect(formatBytes(n)).toBe(want);
  });
});

describe('formatRate', () => {
  it.each([
    [0, '0 B/s'],
    [2048, '2.0 KiB/s'],
    [-1, '0 B/s'],
  ])('%s -> %s', (n, want) => {
    expect(formatRate(n)).toBe(want);
  });
});

describe('formatDuration', () => {
  it.each([
    [0, '0s'],
    [999, '0s'],
    [1000, '1s'],
    [59_000, '59s'],
    [60_000, '1m 0s'],
    [125_000, '2m 5s'],
    [3_600_000, '1h 0m'],
    [3_725_000, '1h 2m'],
    [90_000_000, '1d 1h'],
    [-1, '0s'],
    [Number.NaN, '0s'],
  ])('%s -> %s', (ms, want) => {
    expect(formatDuration(ms)).toBe(want);
  });
});

describe('relativeTime', () => {
  const now = new Date('2026-10-14T12:00:00Z');
  it.each([
    ['2026-10-14T12:00:00Z', 'just now'],
    ['2026-10-14T11:59:30Z', '30s ago'],
    ['2026-10-14T11:55:00Z', '5m ago'],
    ['2026-10-14T09:00:00Z', '3h ago'],
    ['2026-10-12T12:00:00Z', '2d ago'],
    ['2026-10-14T12:00:30Z', 'just now'],
    ['not a date', '—'],
    ['', '—'],
  ])('%s -> %s', (iso, want) => {
    expect(relativeTime(iso, now)).toBe(want);
  });
});

describe('formatPercent', () => {
  it.each([
    [0, '0.0%'],
    [12.345, '12.3%'],
    [100, '100.0%'],
    [-1, '0.0%'],
    [Number.NaN, '0.0%'],
  ])('%s -> %s', (x, want) => {
    expect(formatPercent(x)).toBe(want);
  });
});
