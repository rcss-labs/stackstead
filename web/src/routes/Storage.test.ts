import { fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { StorageImage } from '$lib/api/types';
import { navigate } from '$lib/router';
import { consumer, image, volume } from '$lib/storage/fixtures';
import { installFakeFetch, type FakeFetch } from '../vitest-setup';
import Storage from './Storage.svelte';
const totals = { count: 3, size: null, reclaimable: null, sizesAt: null, refreshing: true, error: '' };
const overview = { images: totals, containers: totals, volumes: totals, buildCache: totals, topContainers: [{ name: 'db', stack: 'media', sizeRw: 1024 }], declarationsUnknown: ['media'] };
let fake: FakeFetch;
let imageRows: StorageImage[];
let refresh: { status?: number; body: unknown };
beforeEach(() => {
  imageRows = [image(), image({ id: 'sha256:' + 'b'.repeat(64), tags: [], dangling: true, status: 'dangling' })];
  navigate('/storage'); refresh = { body: { startedAt: new Date().toISOString() } };
  fake = installFakeFetch({
    'GET /api/storage': { body: overview },
    'GET /api/storage/images': () => ({ body: { images: imageRows, totals } }),
    'GET /api/storage/volumes': { body: { volumes: [volume(), volume({ name: 'free', protection: null, declaredBy: [] }), volume({ name: 'used', protection: null, declaredBy: [], containers: [consumer] })], totals } },
    'GET /api/storage/build-cache': { body: { records: [{ id: 'cache', type: 'regular', description: 'builder cache', size: 1024, inUse: false, shared: true, createdAt: '2026-01-01T00:00:00Z', lastUsedAt: null, usageCount: 1 }], totals } },
    'GET /api/actions?kind=storage.&limit=20': { body: [{ id: 1, ts: '2026-01-01T00:00:00Z', username: 'admin', kind: 'storage.delete', scope: 'host', target: 'old-image', detail: '<script>bad</script>', bytes: null, result: 'succeeded', operationId: 'op' }] },
    'POST /api/storage/refresh': () => refresh,
  });
});
afterEach(() => { fake.restore(); vi.useRealTimers(); });
it('overview shows four pending tiles, declarations warning, largest layers and recent actions safely', async () => {
  render(Storage); await screen.findByRole('heading', { name: 'Storage', level: 1 });
  await screen.findByText('Declarations unknown for: media');
  expect(screen.getAllByText('Container layers').length).toBeGreaterThan(0); expect(screen.getAllByText('calculating…').length).toBeGreaterThanOrEqual(4);
  expect(screen.getByRole('link', { name: 'db' }).getAttribute('href')).toBe('/containers/db');
  expect(await screen.findByText('old-image')).toBeTruthy(); expect(screen.getByText('admin')).toBeTruthy(); expect(screen.getByText('storage.delete')).toBeTruthy();
  expect(screen.getByText('succeeded')).toBeTruthy(); expect(document.querySelector('script')).toBeNull();
});
it('selects query tabs, filters with counted chips and search; invalid tab returns overview', async () => {
  navigate('/storage?tab=images'); render(Storage); await screen.findByRole('link', { name: 'redis:7' });
  expect(screen.getByRole('tab', { name: 'Images' }).getAttribute('aria-selected')).toBe('true');
  expect(screen.queryByRole('button', { name: /^Protected/ })).toBeNull();
  const dangling = screen.getByRole('button', { name: /^Dangling/ }); expect(dangling.textContent).toContain('1');
  await fireEvent.click(dangling); expect(screen.queryByRole('link', { name: 'redis:7' })).toBeNull(); expect(dangling.getAttribute('aria-pressed')).toBe('true');
  await fireEvent.input(screen.getByRole('searchbox', { name: 'Search images' }), { target: { value: 'missing' } }); expect(screen.getByText('No images match the current filter.')).toBeTruthy();
  await fireEvent.click(screen.getByRole('tab', { name: 'Build cache' })); expect(window.location.search).toBe('?tab=build-cache'); expect(await screen.findByText('builder cache')).toBeTruthy();
  navigate('/storage?tab=invalid'); await screen.findAllByText('Container layers');
});
it('shows protected stopped volumes with stack note and no Delete; unused cleanup is enabled', async () => {
  navigate('/storage?tab=volumes'); render(Storage); const name = await screen.findByRole('link', { name: 'media_db' }); const row = name.closest('tr')!;
  expect(within(row).getByText('unused · protected')).toBeTruthy(); expect(within(row).getByText(/declared by stack/)).toBeTruthy(); expect(within(row).getAllByRole('link', { name: 'media' })[0].getAttribute('href')).toBe('/stacks/media');
  expect(within(row).getAllByText(/stopped/).length).toBeGreaterThan(0); expect(within(row).queryByRole('button', { name: 'Delete' })).toBeNull();
  expect(within(row).getByText('calculating…')).toBeTruthy();
  expect(screen.getByRole('button', { name: /Clean up unused volumes/ }).getAttribute('aria-disabled')).toBeNull();
  expect(within(screen.getByRole('link', { name: 'used' }).closest('tr')!).queryByRole('button', { name: 'Delete' })).toBeNull();
  expect(screen.getByRole('button', { name: 'Delete' }).getAttribute('aria-disabled')).toBeNull();
});
it('Recalculate starts refresh, prevents another use for 60 seconds and explains why', async () => {
  vi.useFakeTimers(); render(Storage); await vi.advanceTimersByTimeAsync(0);
  const button = screen.getByRole('button', { name: 'Recalculate' }); await fireEvent.click(button); await vi.advanceTimersByTimeAsync(0);
  expect(button.getAttribute('aria-disabled')).toBe('true'); expect(button.title).toMatch(/60s/); await fireEvent.click(button);
  expect(fake.calls.filter(c => c.path === '/api/storage/refresh')).toHaveLength(1);
  await vi.advanceTimersByTimeAsync(60000); expect(button.getAttribute('aria-disabled')).toBeNull();
});
it('preserves the server 429 message and retry cooldown', async () => {
  refresh = { status: 429, body: { error: 'Size reading already requested; try later', retryAfter: 42 } }; render(Storage);
  const button = await screen.findByRole('button', { name: 'Recalculate' }); await fireEvent.click(button);
  expect(await screen.findByText('Size reading already requested; try later')).toBeTruthy(); await waitFor(() => expect(button.title).toMatch(/42s/));
});

const mib = 1024 ** 2;
it.each([
  { size: 64 * mib, sharedSize: 32 * mib, expected: '~152.0 MiB–544.0 MiB' },
  { size: null, sharedSize: 32 * mib, expected: 'reclaim unknown' },
  { size: 64 * mib, sharedSize: null, expected: 'reclaim unknown' },
])('shows the dangling cleanup range or unknown for candidate measurements $size/$sharedSize', async ({ size, sharedSize, expected }) => {
  imageRows = [
    image({ tags: [], dangling: true, status: 'dangling', size: 480 * mib, sharedSize: 360 * mib }),
    image({ id: 'sha256:' + 'b'.repeat(64), tags: [], dangling: true, status: 'dangling', size, sharedSize }),
    image({ id: 'sha256:' + 'c'.repeat(64), size: 900 * mib, sharedSize: 0 }),
    image({ id: 'sha256:' + 'd'.repeat(64), tags: [], dangling: true, status: 'in-use', containers: [consumer], size: null, sharedSize: null }),
  ];
  navigate('/storage?tab=images'); render(Storage);
  const button = await screen.findByRole('button', { name: /Clean up dangling images/ });
  expect(button.textContent).toContain(`(2 · ${expected})`);
  await fireEvent.input(screen.getByRole('searchbox', { name: 'Search images' }), { target: { value: 'no match' } });
  expect(button.textContent).toContain(`(2 · ${expected})`);
  expect(button.getAttribute('aria-disabled')).toBeNull();
  expect(fake.calls.every(call => call.method === 'GET')).toBe(true);
});

it('opens a fresh preview from a cleanup control and restores focus on Escape', async () => {
  navigate('/storage?tab=images'); render(Storage);
  const button=await screen.findByRole('button',{name:/Clean up dangling images/}); button.focus(); await fireEvent.click(button);
  await screen.findByRole('dialog'); await screen.findByText('no fake route');
  expect(fake.calls.find(c=>c.path==='/api/storage/plans')?.body).toEqual({kind:'images',selection:'dangling'});
  await fireEvent.keyDown(document,{key:'Escape'}); await waitFor(()=>expect(screen.queryByRole('dialog')).toBeNull());expect(document.activeElement).toBe(button);
});

it('returns focus to a Delete button that was clicked without taking focus (Safari, Firefox on macOS)', async () => {
  navigate('/storage?tab=volumes'); render(Storage);
  await screen.findByRole('link', { name: 'free' }); const button = screen.getByRole('button', { name: 'Delete' });
  expect(document.activeElement).not.toBe(button); await fireEvent.click(button);
  await screen.findByRole('dialog'); await fireEvent.keyDown(document, { key: 'Escape' });
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull()); expect(document.activeElement).toBe(button);
});

const manyImages = (n: number) => Array.from({ length: n }, (_, i) => image({ id: 'sha256:' + String(i).padStart(64, '0'), tags: [`app:${i}`], dangling: i % 2 === 0, status: 'no-containers' }));
it('pages images 50 at a time, counts the whole list, resets on filter and hides the pager when all fit', async () => {
  imageRows = manyImages(120); navigate('/storage?tab=images'); render(Storage);
  await screen.findByRole('link', { name: 'app:0' });
  const table = screen.getByRole('table', { name: 'Images' }); expect(within(table).getAllByRole('link')).toHaveLength(50);
  expect(screen.getByRole('button', { name: /^All/ }).textContent).toContain('120');
  expect(screen.getByText('1–50 of 120').getAttribute('aria-live')).toBe('polite');
  expect(screen.getByRole('button', { name: 'Previous' }).hasAttribute('disabled')).toBe(true);
  await fireEvent.click(screen.getByRole('button', { name: 'Next' }));
  expect(screen.getByText('51–100 of 120')).toBeTruthy(); expect(screen.getByRole('link', { name: 'app:50' })).toBeTruthy(); expect(screen.queryByRole('link', { name: 'app:0' })).toBeNull();
  await fireEvent.click(screen.getByRole('button', { name: 'Next' })); expect(screen.getByText('101–120 of 120')).toBeTruthy();
  expect(screen.getByRole('button', { name: 'Next' }).hasAttribute('disabled')).toBe(true);
  await fireEvent.click(screen.getByRole('button', { name: /^Dangling/ })); expect(screen.getByText('1–50 of 60')).toBeTruthy();
  await fireEvent.click(screen.getByRole('button', { name: 'Next' })); await fireEvent.input(screen.getByRole('searchbox', { name: 'Search images' }), { target: { value: 'app:' } });
  expect(screen.getByText('1–50 of 60')).toBeTruthy();
  await fireEvent.input(screen.getByRole('searchbox', { name: 'Search images' }), { target: { value: 'app:11' } });
  expect(screen.queryByRole('button', { name: 'Next' })).toBeNull();
});
