// Shared fixtures and routes for the Storage screen tests. Each test file declares its own
// vi.mock('$lib/stores/live', ...) exposing `__live` and `__changes` writables (vi.mock is hoisted per file).
import { screen, within } from '@testing-library/react';
import type { UserEvent } from '@testing-library/user-event';
import type { Writable } from '$lib/store';
import type { StorageImage, StorageKindTotals, StorageOverview } from '$lib/api/types';
import { consumer, image, volume } from '$lib/storage/fixtures';
import type { Routes } from '../../vitest-setup';

export { mountAt } from '../containers/containersTestKit';

export const GiB = 1024 ** 3;

export const totals: StorageKindTotals = { count: 3, size: null, reclaimable: null, sizesAt: null, refreshing: true, error: '' };

export const overview: StorageOverview = {
  images: totals,
  containers: totals,
  volumes: totals,
  buildCache: totals,
  topContainers: [{ name: 'db', stack: 'media', sizeRw: 1024 }],
  declarationsUnknown: ['media'],
};

/** Measured sizes: 30 / 60 / 5 / 5 GiB with 1 / 0.5 / 0 / 0.5 GiB reclaimable. */
export const sizedOverview: StorageOverview = {
  ...overview,
  images: { ...totals, size: 30 * GiB, reclaimable: GiB, sizesAt: '2026-01-01T00:00:00Z', refreshing: false },
  volumes: { ...totals, size: 60 * GiB, reclaimable: GiB / 2, sizesAt: '2026-01-01T00:00:00Z', refreshing: false },
  containers: { ...totals, size: 5 * GiB, reclaimable: 0, sizesAt: '2026-01-01T00:00:00Z', refreshing: false },
  buildCache: { ...totals, size: 5 * GiB, reclaimable: GiB / 2, sizesAt: '2026-01-01T00:00:00Z', refreshing: false },
};

/** Mutable module state, as in the Svelte Storage.test.ts: tests reassign these and `routes()` reads them lazily. */
export const state: { imageRows: StorageImage[]; refresh: { status?: number; body: unknown } } = { imageRows: [], refresh: { body: {} } };

export function resetState() {
  state.imageRows = [image(), image({ id: 'sha256:' + 'b'.repeat(64), tags: [], dangling: true, status: 'dangling' })];
  state.refresh = { body: { startedAt: new Date().toISOString() } };
}

export function routes(over: Routes = {}): Routes {
  return {
    'GET /api/storage': { body: overview },
    'GET /api/storage/images': () => ({ body: { images: state.imageRows, totals } }),
    'GET /api/storage/volumes': {
      body: {
        volumes: [volume(), volume({ name: 'free', protection: null, declaredBy: [] }), volume({ name: 'used', protection: null, declaredBy: [], containers: [consumer] })],
        totals,
      },
    },
    'GET /api/storage/build-cache': {
      body: {
        records: [{ id: 'cache', type: 'regular', description: 'builder cache', size: 1024, inUse: false, shared: true, createdAt: '2026-01-01T00:00:00Z', lastUsedAt: null, usageCount: 1 }],
        totals,
      },
    },
    'GET /api/actions?kind=storage.&limit=20': {
      body: [{ id: 1, ts: '2026-01-01T00:00:00Z', username: 'admin', kind: 'storage.delete', scope: 'host', target: 'old-image', detail: '<script>bad</script>', bytes: null, result: 'succeeded', operationId: 'op' }],
    },
    'POST /api/storage/refresh': () => state.refresh,
    ...over,
  };
}

/** The writables behind a file's `vi.mock('$lib/stores/live', ...)`. */
export async function liveMocks() {
  const mod = (await import('$lib/stores/live')) as unknown as {
    __live: Writable<Record<string, unknown>>;
    __changes: Writable<{ project: string; seq: number; resync?: true } | null>;
  };
  return { live: mod.__live, changes: mod.__changes };
}

/** Clicks the kind tile named `label` (not the matching bar segment). */
export async function selectKind(user: UserEvent, label: string) {
  const group = screen.getByRole('group', { name: 'Storage kinds' });
  await user.click(within(group).getByRole('button', { name: new RegExp(`^${label}`) }));
}

export const location = () => screen.getByTestId('location').textContent;
