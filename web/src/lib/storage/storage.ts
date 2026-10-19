import type { StorageImage, StorageVolume, StorageReclaim } from '../api/types';
import { formatBytes } from '../format';

export type ImageChip = 'all' | 'in-use' | 'no-containers' | 'dangling';
export type VolumeChip = 'all' | 'in-use' | 'unused' | 'protected';
export const imageChips: { id: ImageChip; label: string }[] = [{ id: 'all', label: 'All' }, { id: 'in-use', label: 'In use' }, { id: 'no-containers', label: 'No containers' }, { id: 'dangling', label: 'Dangling' }];
export const volumeChips: { id: VolumeChip; label: string }[] = [{ id: 'all', label: 'All' }, { id: 'in-use', label: 'In use' }, { id: 'unused', label: 'Unused' }, { id: 'protected', label: 'Protected' }];
const imageMatches = (row: StorageImage, chip: ImageChip) => chip === 'all' || (chip === 'in-use' ? row.containers.length > 0 : chip === 'no-containers' ? row.containers.length === 0 : row.dangling);
const volumeMatches = (row: StorageVolume, chip: VolumeChip) => chip === 'all' || (chip === 'in-use' ? row.containers.length > 0 : chip === 'unused' ? row.containers.length === 0 : row.protection !== null);
const matches = (fields: string[], query: string) => fields.some(field => field.toLowerCase().includes(query.trim().toLowerCase()));
export const filterImages = (rows: StorageImage[], chip: ImageChip, query: string) => rows.filter(row => imageMatches(row, chip) && matches([row.id, ...row.tags, ...row.digests, ...row.declaredBy.map(d => d.stack), ...row.containers.map(c => c.stack)], query));
export const filterVolumes = (rows: StorageVolume[], chip: VolumeChip, query: string) => rows.filter(row => volumeMatches(row, chip) && matches([row.name, row.driver, row.project, ...row.declaredBy.map(d => d.stack), ...row.containers.flatMap(c => [c.name, c.stack])], query));
export function imageCounts(rows: StorageImage[]): Record<ImageChip, number> {
  return { all: rows.length, 'in-use': rows.filter(r => imageMatches(r, 'in-use')).length, 'no-containers': rows.filter(r => imageMatches(r, 'no-containers')).length, dangling: rows.filter(r => r.dangling).length };
}
export function volumeCounts(rows: StorageVolume[]): Record<VolumeChip, number> {
  return { all: rows.length, 'in-use': rows.filter(r => volumeMatches(r, 'in-use')).length, unused: rows.filter(r => volumeMatches(r, 'unused')).length, protected: rows.filter(r => volumeMatches(r, 'protected')).length };
}
/** Cached candidate sizes give bounds, not an exact promise of freed disk space. */
export function imageReclaimRange(candidates: StorageImage[]): string {
  let min = 0;
  let max = 0;
  for (const image of candidates) {
    if (image.size === null || image.sharedSize === null) return 'reclaim unknown';
    min += Math.max(0, image.size - image.sharedSize);
    max += image.size;
  }
  return `~${formatBytes(min)}–${formatBytes(max)}`;
}

export function reclaimText(reclaim: StorageReclaim): string {
  if (reclaim.min === null || reclaim.max === null) return 'Reclaimable size unknown';
  if (reclaim.min !== reclaim.max) return `Frees between ${formatBytes(reclaim.min)} and ${formatBytes(reclaim.max)}`;
  if (!reclaim.exact) return `Frees about ${formatBytes(reclaim.max)} (not exact)`;
  return `Frees ${formatBytes(reclaim.min)}`;
}
export function deleteDisabledReason(row: StorageImage | StorageVolume, dockerDown = false): string | undefined {
  if (dockerDown) return 'Docker is unavailable';
  if (row.containers.length) return `used by ${row.containers.length} container${row.containers.length === 1 ? '' : 's'}`;
  if ('protection' in row && row.protection) {
    const declaration = row.declaredBy.find(d => d.stack === row.protection?.stack);
    return declaration ? `declared by stack ${declaration.stack} (${declaration.stackState}) — remove via the stack` : `protected by stack ${row.protection.stack} — remove via the stack`;
  }
}
export function imagePath(id: string): string | undefined {
  const hex = (id ?? '').replace(/^sha256:/, '');
  return /^[a-fA-F0-9]{12,64}$/.test(hex) ? `/storage/images/${hex.slice(0, 12)}` : undefined;
}
export const sizeText = (bytes: number | null, pending = false) => bytes === null ? pending ? 'calculating…' : '—' : formatBytes(bytes);
/** Stack states arrive as slugs such as not-deployed. */
export const stateText = (state: string) => state.replaceAll('-', ' ');

export const PAGE_SIZE = 50;
export type PageView<T> = { rows: T[]; page: number; pages: number; from: number; to: number; total: number };
/** Client-side paging; an out-of-range page (for example after a refresh shrank the list) is clamped. */
export function pageView<T>(rows: T[], page: number, size = PAGE_SIZE): PageView<T> {
  const pages = Math.max(1, Math.ceil(rows.length / size));
  const current = Math.min(Math.max(1, Math.trunc(page) || 1), pages);
  const start = (current - 1) * size;
  const shown = rows.slice(start, start + size);
  return { rows: shown, page: current, pages, from: shown.length ? start + 1 : 0, to: start + shown.length, total: rows.length };
}
