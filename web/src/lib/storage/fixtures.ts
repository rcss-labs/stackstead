// Shared storage response fixtures for focused UI tests.
import type { StorageImage, StorageVolume } from '../api/types';
export const consumer = { containerId: 'a', name: 'db', state: 'exited', project: '', service: '', stack: '' };
export const declaration = { stack: 'media', service: 'db', ref: '', stackState: 'stopped' };
export const image = (over: Partial<StorageImage> = {}): StorageImage => ({ id: 'sha256:' + 'a'.repeat(64), tags: ['redis:7'], digests: [], created: '2026-01-01T00:00:00Z', size: null, sharedSize: null, containers: [], declaredBy: [], dangling: false, status: 'no-containers', ...over });
export const volume = (over: Partial<StorageVolume> = {}): StorageVolume => ({ name: 'media_db', driver: 'local', scope: 'local', project: 'media', createdAt: '2026-01-01T00:00:00Z', labels: {}, size: null, refCount: null, sizesPending: true, containers: [], declaredBy: [declaration], protection: { reason: 'declared', stack: 'media' }, status: 'unused-protected', ...over });
