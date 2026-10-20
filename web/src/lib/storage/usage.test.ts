import { describe, expect, it } from 'vitest';
import type { StorageKindTotals, StorageOverview } from '$lib/api/types';
import { cleanupRequest, kindFromQuery, KINDS, reclaimableText, sortBySize, usageSegments, usageSummary, usedText, type StorageKind } from './usage';

const GiB = 1024 ** 3;
const MiB = 1024 ** 2;
const totals = (over: Partial<StorageKindTotals> = {}): StorageKindTotals => ({ count: 3, size: null, reclaimable: null, sizesAt: null, refreshing: true, error: '', ...over });
const overview = (over: Partial<Record<'images' | 'containers' | 'volumes' | 'buildCache', Partial<StorageKindTotals>>> = {}): StorageOverview => ({
  images: totals(over.images), containers: totals(over.containers), volumes: totals(over.volumes), buildCache: totals(over.buildCache),
  topContainers: [], declarationsUnknown: [],
});

describe('kindFromQuery', () => {
  it.each([
    ['images', 'images'], ['volumes', 'volumes'], ['layers', 'layers'], ['build-cache', 'build-cache'],
    ['overview', 'images'], ['invalid', 'images'], [null, 'images'],
  ] as [string | null, StorageKind][])('%s -> %s', (value, expected) => {
    expect(kindFromQuery(value)).toBe(expected);
  });
});

describe('KINDS', () => {
  it('lists the four kinds in order with their labels and colours', () => {
    expect(KINDS.map(k => [k.id, k.label, k.color])).toEqual([
      ['images', 'Images', '#60a5fa'], ['volumes', 'Volumes', '#a78bfa'], ['layers', 'Container layers', '#14b8a6'], ['build-cache', 'Build cache', '#f97316'],
    ]);
  });
});

describe('usageSegments', () => {
  it('shares follow the known sizes and unknown sizes take nothing', () => {
    const segments = usageSegments(overview({ images: { size: 30 * GiB }, volumes: { size: 60 * GiB }, containers: { size: 5 * GiB }, buildCache: { size: null } }));
    expect(segments.map(s => s.kind)).toEqual(['images', 'volumes', 'layers', 'build-cache']);
    expect(segments.map(s => s.share)).toEqual([30 / 95, 60 / 95, 5 / 95, 0]);
    expect(segments.map(s => s.percent)).toEqual([32, 63, 5, 0]);
  });
  it('all unknown sizes split the bar evenly', () => {
    const segments = usageSegments(overview());
    expect(segments.map(s => s.share)).toEqual([0.25, 0.25, 0.25, 0.25]);
    expect(segments.map(s => s.percent)).toEqual([25, 25, 25, 25]);
    expect(segments.map(s => s.bytes)).toEqual([null, null, null, null]);
  });
  it('a zero total splits evenly too', () => {
    const zero = { size: 0 };
    expect(usageSegments(overview({ images: zero, volumes: zero, containers: zero, buildCache: zero })).map(s => s.share)).toEqual([0.25, 0.25, 0.25, 0.25]);
  });
});

describe('usageSummary', () => {
  it('sums used and reclaimable and takes the latest sizesAt', () => {
    const summary = usageSummary(overview({
      images: { size: GiB, reclaimable: 512 * MiB, sizesAt: '2026-01-01T00:00:00Z', refreshing: false },
      volumes: { size: GiB, reclaimable: null, sizesAt: '2026-01-02T00:00:00Z', refreshing: false },
      containers: { size: GiB, reclaimable: 256 * MiB, sizesAt: null, refreshing: false },
      buildCache: { size: GiB, reclaimable: 0, sizesAt: '2026-01-01T12:00:00Z', refreshing: false },
    }));
    expect(summary).toEqual({ used: 4 * GiB, reclaimable: 768 * MiB, sizesAt: '2026-01-02T00:00:00Z', refreshing: false });
  });
  it('used is unknown while any size is unknown', () => {
    const summary = usageSummary(overview({ images: { size: GiB, refreshing: false }, volumes: { size: GiB, refreshing: false }, containers: { size: GiB, refreshing: false }, buildCache: { size: null, refreshing: true } }));
    expect(summary.used).toBeNull();
    expect(summary.refreshing).toBe(true);
  });
});

describe('usedText / reclaimableText', () => {
  const base = { reclaimable: 0, sizesAt: null };
  it('formats the used size', () => expect(usedText({ ...base, used: 4 * GiB, refreshing: false })).toBe('4.0 GiB used by Docker'));
  it('says calculating while sizes are refreshing', () => expect(usedText({ ...base, used: null, refreshing: true })).toBe('Size calculating…'));
  it('says not available when unknown and idle', () => expect(usedText({ ...base, used: null, refreshing: false })).toBe('Size not available yet'));
  it('formats reclaimable bytes', () => expect(reclaimableText({ ...base, used: null, refreshing: false, reclaimable: 768 * MiB })).toBe('768.0 MiB reclaimable'));
  it('says nothing reclaimable at zero', () => expect(reclaimableText({ ...base, used: null, refreshing: false })).toBe('nothing reclaimable'));
});

describe('sortBySize', () => {
  const row = (id: string, size: number | null) => ({ id, size });
  it('orders largest first, unknown last, ties stable', () => {
    const rows = [row('a', null), row('b', 10), row('c', 30), row('d', 10), row('e', null)];
    expect(sortBySize(rows, r => r.size).map(r => r.id)).toEqual(['c', 'b', 'd', 'a', 'e']);
  });
  it('does not mutate the input', () => {
    const rows = [row('a', 1), row('b', 2)];
    const sorted = sortBySize(rows, r => r.size);
    expect(sorted).not.toBe(rows);
    expect(rows.map(r => r.id)).toEqual(['a', 'b']);
  });
});

describe('cleanupRequest', () => {
  it.each([
    ['images', { kind: 'images', selection: 'dangling' }],
    ['volumes', { kind: 'volumes', selection: 'unused' }],
    ['build-cache', { kind: 'build-cache', selection: 'unused' }],
    ['layers', undefined],
  ] as [StorageKind, unknown][])('%s', (kind, expected) => {
    expect(cleanupRequest(kind)).toEqual(expected);
  });
});
