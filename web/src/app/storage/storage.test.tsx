import { act, cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { consumer, image } from '$lib/storage/fixtures';
import { refreshRunningOps } from '$lib/stores/stacks';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { findRoute } from '../routes';
import { liveMocks, location, mountAt, overview, resetState, routes, selectKind, sizedOverview, state, totals } from './storageTestKit';

vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const live = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  const changes = writable<{ project: string; seq: number; resync?: true } | null>(null);
  return {
    ...(await orig<typeof import('$lib/stores/live')>()),
    live: { subscribe: live.subscribe },
    changes: { subscribe: changes.subscribe },
    refreshStatus: vi.fn(),
    __live: live,
    __changes: changes,
  };
});

let fake: FakeFetch;
const calls = (path: string) => fake.calls.filter((c) => c.path === path);

beforeEach(() => {
  resetState();
  fake = installFakeFetch(routes());
});

afterEach(async () => {
  cleanup();
  fake.restore();
  vi.useRealTimers();
  // Live state is shared module state; restore it here so a failing assertion cannot leak Docker-down into later tests.
  const { live } = await liveMocks();
  live.update((l) => ({ ...l, docker: 'ok' }));
});

describe('Storage screen', () => {
  it('shows four tiles with pending sizes, the declarations warning, the layers table and recent actions safely', async () => {
    const user = userEvent.setup();
    mountAt('/storage');
    await screen.findByRole('heading', { name: 'Storage', level: 1 });
    await screen.findByText('Declarations unknown for: media');
    expect(screen.getAllByText('calculating…').length).toBeGreaterThanOrEqual(4);
    const tiles = within(screen.getByRole('group', { name: 'Storage kinds' })).getAllByRole('button');
    expect(tiles.filter((b) => b.hasAttribute('aria-pressed'))).toHaveLength(4);
    expect(within(screen.getByRole('group', { name: 'Storage kinds' })).getByRole('button', { name: /^Container layers/ })).toBeTruthy();
    await selectKind(user, 'Container layers');
    expect(screen.getByRole('link', { name: 'db' }).getAttribute('href')).toBe('/containers/db');
    expect(screen.getByRole('link', { name: 'media' }).getAttribute('href')).toBe('/stacks/media');
    expect(screen.getByText('1.0 KiB')).toBeTruthy();
    expect(await screen.findByText('old-image')).toBeTruthy();
    expect(screen.getByText('admin')).toBeTruthy();
    expect(screen.getByText('storage.delete')).toBeTruthy();
    expect(screen.getByText('succeeded')).toBeTruthy();
    expect(document.querySelector('script')).toBeNull();
  });

  it('the lead sums used and reclaimable and dates the sizes', async () => {
    fake.restore();
    fake = installFakeFetch(routes({ 'GET /api/storage': { body: sizedOverview } }));
    mountAt('/storage');
    await screen.findByText('100.0 GiB used by Docker');
    expect(screen.getByText('2.0 GiB reclaimable')).toBeTruthy();
    expect(screen.getByText(/sizes from/)).toBeTruthy();
  });

  it('the lead says calculating and shows no reclaimable text while sizes are unknown', async () => {
    mountAt('/storage');
    const lead = (await screen.findByText(/Size calculating…/)).closest('p')!;
    expect(lead.textContent).not.toMatch(/reclaimable/);
  });

  it('the lead says nothing reclaimable when a known value is zero', async () => {
    const zero = { ...totals, size: 1024 ** 3, reclaimable: 0, refreshing: false, sizesAt: '2026-01-01T00:00:00Z' };
    fake.restore();
    fake = installFakeFetch(routes({ 'GET /api/storage': { body: { ...overview, images: zero, volumes: zero, containers: zero, buildCache: zero } } }));
    mountAt('/storage');
    await screen.findByText('nothing reclaimable');
  });

  it('the usage bar segments select a kind by click and by keyboard, through the URL, and the table announces it', async () => {
    fake.restore();
    fake = installFakeFetch(routes({ 'GET /api/storage': { body: sizedOverview } }));
    const user = userEvent.setup();
    mountAt('/storage');
    const bar = await screen.findByRole('group', { name: 'Usage by kind' });
    expect(within(bar).getAllByRole('button')).toHaveLength(4);
    expect(within(bar).getByRole('button', { name: 'Images · 30.0 GiB · 30%' }).getAttribute('aria-pressed')).toBe('true');
    await user.click(within(bar).getByRole('button', { name: 'Volumes · 60.0 GiB · 60%' }));
    expect(location()).toBe('/storage?tab=volumes');
    expect(within(screen.getByRole('group', { name: 'Storage kinds' })).getByRole('button', { name: /^Volumes/ }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByRole('status', { name: 'Selected storage kind' }).textContent).toBe('3 volumes · sorted by size');
    await user.click(within(bar).getByRole('button', { name: /^Container layers/ }));
    expect(location()).toBe('/storage?tab=layers');
    expect(screen.getByRole('status', { name: 'Selected storage kind' }).textContent).toBe('1 container layer · sorted by size');
    act(() => within(bar).getByRole('button', { name: /^Build cache/ }).focus());
    await user.keyboard('{Enter}');
    expect(location()).toBe('/storage?tab=build-cache');
  });

  it('an all-unknown overview still renders four reachable segments', async () => {
    mountAt('/storage');
    const bar = await screen.findByRole('group', { name: 'Usage by kind' });
    const segments = within(bar).getAllByRole('button');
    expect(segments).toHaveLength(4);
    // The 25% is only a placeholder share, so it must not be announced as a measurement.
    for (const segment of segments) expect(segment.getAttribute('aria-label')).not.toMatch(/%/);
  });

  it('one persistent status region changes its text when the kind changes', async () => {
    const user = userEvent.setup();
    mountAt('/storage');
    const region = await screen.findByRole('status', { name: 'Selected storage kind' });
    const before = region.textContent;
    await selectKind(user, 'Container layers');
    expect(screen.getByRole('status', { name: 'Selected storage kind' })).toBe(region);
    expect(region.textContent).toBe('1 container layer · sorted by size');
    expect(region.textContent).not.toBe(before);
  });

  it('lists Recent storage actions once in heading navigation, with data and when empty', async () => {
    mountAt('/storage');
    await screen.findByText('old-image');
    expect(screen.getAllByRole('heading', { name: 'Recent storage actions' })).toHaveLength(1);
    expect(screen.getByRole('region', { name: 'Recent storage actions' })).toBeTruthy();
    cleanup();
    fake.restore();
    fake = installFakeFetch(routes({ 'GET /api/actions?kind=storage.&limit=20': { body: [] } }));
    mountAt('/storage');
    await screen.findByText('No storage actions yet.');
    expect(screen.getAllByRole('heading', { name: 'Recent storage actions' })).toHaveLength(1);
    expect(screen.getByRole('region', { name: 'Recent storage actions' })).toBeTruthy();
  });

  it('selects the kind from ?tab, filters with counted chips and search; an unknown tab selects Images', async () => {
    const user = userEvent.setup();
    mountAt('/storage?tab=images');
    await screen.findByRole('link', { name: 'redis:7' });
    const kinds = within(screen.getByRole('group', { name: 'Storage kinds' }));
    expect(kinds.getByRole('button', { name: /^Images/ }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.queryByRole('button', { name: /^Protected/ })).toBeNull();
    const dangling = screen.getByRole('button', { name: /^Dangling/ });
    expect(dangling.textContent).toContain('1');
    await user.click(dangling);
    expect(screen.queryByRole('link', { name: 'redis:7' })).toBeNull();
    expect(dangling.getAttribute('aria-pressed')).toBe('true');
    await user.type(screen.getByRole('searchbox', { name: 'Search images' }), 'missing');
    expect(screen.getByText('No images match the current filter.')).toBeTruthy();
    await selectKind(user, 'Build cache');
    expect(location()).toBe('/storage?tab=build-cache');
    expect(await screen.findByText('builder cache')).toBeTruthy();
    cleanup();
    mountAt('/storage?tab=invalid');
    await screen.findByRole('link', { name: 'redis:7' });
    expect(within(screen.getByRole('group', { name: 'Storage kinds' })).getByRole('button', { name: /^Images/ }).getAttribute('aria-pressed')).toBe('true');
  });

  it('shows protected stopped volumes with stack note and no Delete; unused cleanup is enabled', async () => {
    mountAt('/storage?tab=volumes');
    const row = (await screen.findByRole('link', { name: 'media_db' })).closest('[role="row"]') as HTMLElement;
    expect(within(row).getByText('unused · protected')).toBeTruthy();
    expect(within(row).getByText(/declared by stack/)).toBeTruthy();
    expect(within(row).getAllByRole('link', { name: 'media' })[0].getAttribute('href')).toBe('/stacks/media');
    expect(within(row).getAllByText(/stopped/).length).toBeGreaterThan(0);
    expect(within(row).queryByRole('button', { name: 'Delete' })).toBeNull();
    expect(within(row).getByText('calculating…')).toBeTruthy();
    // The note explains the missing Delete at every width, so it must also sit in the name cell, which is never hidden.
    const nameCell = within(row).getAllByRole('cell')[0];
    expect(within(nameCell).getByText(/remove via the stack/)).toBeTruthy();
    expect(screen.getByRole('button', { name: /Clean up unused volumes/ }).getAttribute('aria-disabled')).toBeNull();
    const used = screen.getByRole('link', { name: 'used' }).closest('[role="row"]') as HTMLElement;
    expect(within(used).queryByRole('button', { name: 'Delete' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Delete' }).getAttribute('aria-disabled')).toBeNull();
  });

  it('Docker down soft-disables every cleanup control with a reason', async () => {
    const { live } = await liveMocks();
    mountAt('/storage?tab=volumes');
    await screen.findByRole('link', { name: 'free' });
    act(() => live.update((l) => ({ ...l, docker: 'unavailable' })));
    const bulk = screen.getByRole('button', { name: /Clean up dangling images/ });
    const row = screen.getByRole('link', { name: 'free' }).closest('[role="row"]') as HTMLElement;
    const del = within(row).getByRole('button', { name: 'Delete' });
    for (const control of [bulk, del]) {
      await waitFor(() => expect(control.getAttribute('aria-disabled')).toBe('true'));
      expect(control.title).toBe('Docker is unavailable');
    }
  });

  const mib = 1024 ** 2;
  it.each([
    { size: 64 * mib, sharedSize: 32 * mib, expected: '~152.0 MiB–544.0 MiB' },
    { size: null, sharedSize: 32 * mib, expected: 'reclaim unknown' },
    { size: 64 * mib, sharedSize: null, expected: 'reclaim unknown' },
  ])('shows the dangling cleanup range or unknown for candidate measurements $size/$sharedSize', async ({ size, sharedSize, expected }) => {
    state.imageRows = [
      image({ tags: [], dangling: true, status: 'dangling', size: 480 * mib, sharedSize: 360 * mib }),
      image({ id: 'sha256:' + 'b'.repeat(64), tags: [], dangling: true, status: 'dangling', size, sharedSize }),
      image({ id: 'sha256:' + 'c'.repeat(64), size: 900 * mib, sharedSize: 0 }),
      image({ id: 'sha256:' + 'd'.repeat(64), tags: [], dangling: true, status: 'in-use', containers: [consumer], size: null, sharedSize: null }),
    ];
    const user = userEvent.setup();
    mountAt('/storage?tab=images');
    const button = await screen.findByRole('button', { name: /Clean up dangling images/ });
    expect(button.textContent).toContain(`(2 · ${expected})`);
    await user.type(screen.getByRole('searchbox', { name: 'Search images' }), 'no match');
    expect(button.textContent).toContain(`(2 · ${expected})`);
    expect(button.getAttribute('aria-disabled')).toBeNull();
    expect(fake.calls.every((call) => call.method === 'GET')).toBe(true);
  });

  it('rows are sorted largest first with unknown sizes last', async () => {
    state.imageRows = [
      image({ id: 'sha256:' + '1'.repeat(64), tags: ['a:1'], size: 1024 ** 3 }),
      image({ id: 'sha256:' + '2'.repeat(64), tags: ['b:1'], size: null }),
      image({ id: 'sha256:' + '3'.repeat(64), tags: ['c:1'], size: 3 * 1024 ** 3 }),
    ];
    mountAt('/storage?tab=images');
    await screen.findByRole('link', { name: 'a:1' });
    const links = within(screen.getByRole('table', { name: 'Images' })).getAllByRole('link');
    expect(links.slice(0, 3).map((l) => l.textContent)).toEqual(['c:1', 'a:1', 'b:1']);
  });

  it('opens a fresh preview from a cleanup control and restores focus on Escape', async () => {
    mountAt('/storage?tab=images');
    const button = await screen.findByRole('button', { name: /Clean up dangling images/ });
    act(() => button.focus());
    fireEvent.click(button);
    await screen.findByRole('dialog');
    await screen.findByText('no fake route');
    expect(fake.calls.find((c) => c.path === '/api/storage/plans')?.body).toEqual({ kind: 'images', selection: 'dangling' });
    fireEvent.keyDown(document, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(document.activeElement).toBe(button);
  });

  it('returns focus to a Delete button that was clicked without taking focus (Safari, Firefox on macOS)', async () => {
    mountAt('/storage?tab=volumes');
    const row = (await screen.findByRole('link', { name: 'free' })).closest('[role="row"]') as HTMLElement;
    const button = within(row).getByRole('button', { name: 'Delete' });
    expect(document.activeElement).not.toBe(button);
    fireEvent.click(button);
    await screen.findByRole('dialog');
    fireEvent.keyDown(document, { key: 'Escape' });
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(document.activeElement).toBe(button);
  });

  it('a single image Delete previews by immutable id', async () => {
    mountAt('/storage?tab=images');
    const row = (await screen.findByRole('link', { name: 'redis:7' })).closest('[role="row"]') as HTMLElement;
    fireEvent.click(within(row).getByRole('button', { name: 'Delete' }));
    await screen.findByRole('dialog');
    await waitFor(() => expect(fake.calls.find((c) => c.path === '/api/storage/plans')?.body).toEqual({ kind: 'image', ref: 'sha256:' + 'a'.repeat(64) }));
  });

  it('pages images 50 at a time, counts the whole list, resets on filter and kind change and hides the pager when all fit', async () => {
    state.imageRows = Array.from({ length: 120 }, (_, i) => image({ id: 'sha256:' + String(i).padStart(64, '0'), tags: [`app:${i}`], dangling: i % 2 === 0, status: 'no-containers' }));
    const user = userEvent.setup();
    mountAt('/storage?tab=images');
    await screen.findByRole('link', { name: 'app:0' });
    expect(within(screen.getByRole('table', { name: 'Images' })).getAllByRole('link')).toHaveLength(50);
    expect(screen.getByRole('button', { name: /^All/ }).textContent).toContain('120');
    expect(screen.getByText('1–50 of 120').getAttribute('aria-live')).toBe('polite');
    expect(screen.getByRole('button', { name: 'Previous' }).getAttribute('disabled')).not.toBeNull();
    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByText('51–100 of 120')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'app:50' })).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'app:0' })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByText('101–120 of 120')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Next' }).getAttribute('disabled')).not.toBeNull();
    await user.click(screen.getByRole('button', { name: /^Dangling/ }));
    expect(screen.getByText('1–50 of 60')).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByText('51–60 of 60')).toBeTruthy();
    await user.type(screen.getByRole('searchbox', { name: 'Search images' }), 'app:');
    expect(screen.getByText('1–50 of 60')).toBeTruthy();
    await user.type(screen.getByRole('searchbox', { name: 'Search images' }), '11');
    expect(screen.queryByRole('button', { name: 'Next' })).toBeNull();
    await user.clear(screen.getByRole('searchbox', { name: 'Search images' }));
    await user.click(screen.getByRole('button', { name: 'Next' }));
    expect(screen.getByText('51–60 of 60')).toBeTruthy();
    // Page 2 is showing now, so only the kind change can bring back page 1.
    await selectKind(user, 'Volumes');
    await selectKind(user, 'Images');
    expect((screen.getByRole('searchbox', { name: 'Search images' }) as HTMLInputElement).value).toBe('');
    // Chips are not part of the reset (as in the Svelte screen): Dangling is still pressed until All is chosen.
    expect(screen.getByText('1–50 of 60')).toBeTruthy();
    await user.click(screen.getByRole('button', { name: /^All/ }));
    expect(screen.getByText('1–50 of 120')).toBeTruthy();
  });

  it('a failed images list shows the error inside the table card with Retry while the usage card stays', async () => {
    fake.restore();
    fake = installFakeFetch(routes({ 'GET /api/storage/images': { status: 500, body: { error: 'no docker' } } }));
    const user = userEvent.setup();
    mountAt('/storage?tab=images');
    await screen.findByText('Could not load storage');
    expect(screen.getByText('no docker')).toBeTruthy();
    expect(screen.getByRole('group', { name: 'Storage kinds' })).toBeTruthy();
    // Without the list there is no candidate count to show, so the bulk cleanup says why it is unavailable.
    const bulk = screen.getByRole('button', { name: /Clean up dangling images/ });
    expect(bulk.getAttribute('aria-disabled')).toBe('true');
    expect(bulk.title).toBe('The image list could not be loaded');
    expect(bulk.textContent).not.toMatch(/\(0 ·/);
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(calls('/api/storage/images')).toHaveLength(2));
  });

  it('counts in the status text are singular for one', async () => {
    const user = userEvent.setup();
    mountAt('/storage?tab=images');
    const region = await screen.findByRole('status', { name: 'Selected storage kind' });
    await waitFor(() => expect(region.textContent).toBe('2 images · sorted by size'));
    await user.click(screen.getByRole('button', { name: /^Dangling/ }));
    expect(region.textContent).toBe('1 image · sorted by size');
    await selectKind(user, 'Container layers');
    expect(region.textContent).toBe('1 container layer · sorted by size');
    await selectKind(user, 'Build cache');
    expect(region.textContent).toBe('1 record · sorted by size');
  });

  it('the mobile sub-line names the status and the first consumer', async () => {
    mountAt('/storage?tab=volumes');
    const used = (await screen.findByRole('link', { name: 'used' })).closest('[role="row"]') as HTMLElement;
    expect(within(used).getByText('in use · db')).toBeTruthy();
    const free = screen.getByRole('link', { name: 'free' }).closest('[role="row"]') as HTMLElement;
    expect(within(free).getByText('unused · —')).toBeTruthy();
  });

  it('Recalculate starts refresh, prevents another use for 60 seconds and explains why', async () => {
    vi.useFakeTimers();
    mountAt('/storage');
    await act(() => vi.advanceTimersByTimeAsync(0));
    const button = screen.getByRole('button', { name: 'Recalculate' });
    fireEvent.click(button);
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(button.getAttribute('aria-disabled')).toBe('true');
    expect(button.title).toMatch(/60s/);
    fireEvent.click(button);
    expect(calls('/api/storage/refresh')).toHaveLength(1);
    await act(() => vi.advanceTimersByTimeAsync(60000));
    expect(button.getAttribute('aria-disabled')).toBeNull();
  });

  it('preserves the server 429 message and retry cooldown', async () => {
    const { state } = await import('./storageTestKit');
    state.refresh = { status: 429, body: { error: 'Size reading already requested; try later', retryAfter: 42 } };
    const user = userEvent.setup();
    mountAt('/storage');
    const button = await screen.findByRole('button', { name: 'Recalculate' });
    await user.click(button);
    expect(await screen.findByText('Size reading already requested; try later')).toBeTruthy();
    await waitFor(() => expect(button.title).toMatch(/42s/));
  });

  it('disables Recalculate while Docker is unavailable', async () => {
    const { live } = await liveMocks();
    mountAt('/storage');
    const button = await screen.findByRole('button', { name: 'Recalculate' });
    act(() => live.update((l) => ({ ...l, docker: 'unavailable' })));
    await waitFor(() => expect(button.title).toBe('Docker is unavailable'));
  });

  it('a failed overview shows the error with Retry and keeps the page', async () => {
    fake.restore();
    fake = installFakeFetch(routes({ 'GET /api/storage': { status: 500, body: { error: 'no docker' } } }));
    const user = userEvent.setup();
    mountAt('/storage');
    await screen.findByText('Could not load storage');
    expect(screen.getByText('no docker')).toBeTruthy();
    expect(screen.getByRole('region', { name: 'Recent storage actions' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(calls('/api/storage')).toHaveLength(2));
  });

  it('refetches on a change event and when operations end', async () => {
    const { changes } = await liveMocks();
    mountAt('/storage');
    await screen.findByText('Declarations unknown for: media');
    expect(calls('/api/storage/images')).toHaveLength(1);
    act(() => changes.set({ project: '', seq: 1 }));
    await waitFor(() => expect(calls('/api/storage/images')).toHaveLength(2));
    act(() => refreshRunningOps());
    await waitFor(() => expect(calls('/api/storage/images')).toHaveLength(3));
  });

  it('the route table titles /storage as Storage', () => {
    expect(findRoute('/storage')?.title).toBe('Storage');
  });
});
