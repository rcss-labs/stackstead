import type { StorageKindTotals, StorageOverview, StoragePlanRequest } from '../api/types';
import { reclaimableTotal } from '../dashboard/issues';
import { formatBytes } from '../format';

export type StorageKind = 'images' | 'volumes' | 'layers' | 'build-cache';

export interface KindInfo {
  id: StorageKind;
  label: string;
  /** Used only through KINDS; components expose it to CSS as a `--kind` custom property. */
  color: string;
  totals: (o: StorageOverview) => StorageKindTotals;
}

export const KINDS: readonly KindInfo[] = [
  { id: 'images', label: 'Images', color: '#60a5fa', totals: o => o.images },
  { id: 'volumes', label: 'Volumes', color: '#a78bfa', totals: o => o.volumes },
  { id: 'layers', label: 'Container layers', color: '#14b8a6', totals: o => o.containers },
  { id: 'build-cache', label: 'Build cache', color: '#f97316', totals: o => o.buildCache },
];

/** The `?tab` value as a kind; anything else (including the old `overview`) falls back to images. */
export function kindFromQuery(value: string | null): StorageKind {
  return KINDS.find(k => k.id === value)?.id ?? 'images';
}

export interface UsageSegment {
  kind: StorageKind;
  bytes: number | null;
  share: number;
  percent: number;
}

export function usageSegments(overview: StorageOverview): UsageSegment[] {
  const sizes = KINDS.map(k => ({ kind: k.id, bytes: k.totals(overview).size }));
  const sum = sizes.reduce((total, s) => total + (s.bytes ?? 0), 0);
  return sizes.map(s => {
    const share = sum > 0 ? (s.bytes ?? 0) / sum : 0.25;
    return { ...s, share, percent: Math.round(share * 100) };
  });
}

export interface UsageSummary {
  used: number | null;
  reclaimable: number;
  sizesAt: string | null;
  refreshing: boolean;
}

export function usageSummary(overview: StorageOverview): UsageSummary {
  const all = KINDS.map(k => k.totals(overview));
  const sizes = all.map(t => t.size);
  const stamps = all.map(t => t.sizesAt).filter((at): at is string => at !== null);
  return {
    used: sizes.every((s): s is number => s !== null) ? sizes.reduce((a, b) => a + b, 0) : null,
    reclaimable: reclaimableTotal(overview),
    sizesAt: stamps.length ? stamps.reduce((a, b) => (b > a ? b : a)) : null,
    refreshing: all.some(t => t.refreshing),
  };
}

export function usedText(summary: UsageSummary): string {
  if (summary.used !== null) return `${formatBytes(summary.used)} used by Docker`;
  return summary.refreshing ? 'Size calculating…' : 'Size not available yet';
}

export function reclaimableText(summary: UsageSummary): string {
  return summary.reclaimable === 0 ? 'nothing reclaimable' : `${formatBytes(summary.reclaimable)} reclaimable`;
}

/** Largest first, unknown sizes last; ties keep their input order. Returns a new array. */
export function sortBySize<T>(rows: T[], size: (row: T) => number | null): T[] {
  return rows
    .map((row, index) => ({ row, index, bytes: size(row) }))
    .sort((a, b) => {
      if (a.bytes === null || b.bytes === null) {
        if (a.bytes === b.bytes) return a.index - b.index;
        return a.bytes === null ? 1 : -1;
      }
      return b.bytes - a.bytes || a.index - b.index;
    })
    .map(entry => entry.row);
}

/** The bulk cleanup each kind offers; container layers have none. */
export function cleanupRequest(kind: StorageKind): StoragePlanRequest | undefined {
  switch (kind) {
    case 'images': return { kind: 'images', selection: 'dangling' };
    case 'volumes': return { kind: 'volumes', selection: 'unused' };
    case 'build-cache': return { kind: 'build-cache', selection: 'unused' };
    case 'layers': return undefined;
  }
}
