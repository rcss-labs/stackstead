import { describe, expect, it } from 'vitest';
import { filterImages, filterVolumes, imageCounts, volumeCounts, reclaimText, deleteDisabledReason, imagePath } from './storage';
import type { StorageImage, StorageVolume } from '../api/types';
import { consumer, declaration, image, volume } from './fixtures';
describe('storage presentation rules', () => {
  it('counts overlapping chips and searches image tags, ids and declared stacks', () => {
    const rows = [image(), image({ id: 'bbb', tags: [], dangling: true, status: 'dangling' }), image({ containers: [consumer], declaredBy: [declaration], status: 'in-use' })];
    expect(imageCounts(rows)).toEqual({ all: 3, 'in-use': 1, 'no-containers': 2, dangling: 1 });
    expect(filterImages(rows, 'no-containers', ' REDIS ')).toEqual([rows[0]]);
    expect(filterImages(rows, 'all', 'MEDIA')).toEqual([rows[2]]);
    expect(filterImages(rows, 'dangling', 'bbb')).toEqual([rows[1]]);
  });
  it('counts protected unused volumes without making them deletable and searches owners/consumers', () => {
    const rows = [volume(), volume({ name: 'free', declaredBy: [], protection: null }), volume({ name: 'used', containers: [consumer], status: 'in-use' })];
    expect(volumeCounts(rows)).toEqual({ all: 3, 'in-use': 1, unused: 2, protected: 2 });
    expect(filterVolumes(rows, 'unused', 'MEDIA_DB')).toEqual([rows[0]]);
    expect(filterVolumes(rows, 'in-use', 'db')).toEqual([rows[2]]);
    expect(deleteDisabledReason(rows[0])).toMatch(/declared by stack media/);
    expect(deleteDisabledReason(rows[1])).toBeUndefined();
    expect(deleteDisabledReason(rows[2])).toBe('used by 1 container');
    expect(deleteDisabledReason(image({ containers: [consumer, consumer] }))).toBe('used by 2 containers');
    expect(deleteDisabledReason(image(), true)).toBe('Docker is unavailable');
  });
  it.each([
    [{ min: 1024 ** 3, max: 1024 ** 3, exact: true }, 'Frees 1.0 GiB'],
    [{ min: 120 * 1024 ** 2, max: 480 * 1024 ** 2, exact: true }, 'Frees between 120.0 MiB and 480.0 MiB'],
    [{ min: 2 * 1024 ** 3, max: 2 * 1024 ** 3, exact: false }, 'Frees about 2.0 GiB (not exact)'],
    [{ min: null, max: null, exact: false }, 'Reclaimable size unknown'],
  ])('labels reclaim estimates truthfully', (value, text) => expect(reclaimText(value)).toBe(text));
  it('uses a hex immutable image path and refuses missing or malformed ids', () => {
    expect(imagePath('sha256:' + 'f'.repeat(64))).toBe('/storage/images/' + 'f'.repeat(12));
    expect(imagePath('redis:7')).toBeUndefined(); expect(imagePath('')).toBeUndefined();
  });
});

it('searches the registered stack of consumers even when the image has no declaration', () => {
  const usedImage = image({ containers: [{ ...consumer, stack: 'registered-stack' }] });
  const usedVolume = volume({ declaredBy: [], protection: null, containers: [{ ...consumer, stack: 'registered-stack' }] });
  expect(filterImages([usedImage], 'all', 'REGISTERED-STACK')).toEqual([usedImage]);
  expect(filterVolumes([usedVolume], 'all', 'registered-stack')).toEqual([usedVolume]);
});
