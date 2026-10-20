import { act, cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { refreshRunningOps } from '$lib/stores/stacks';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { findRoute } from '../routes';
import { liveMocks, location, mountAt, overview, resetState, routes, selectKind, sizedOverview, totals } from './storageTestKit';

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
    await user.click(within(bar).getByRole('button', { name: /^Container layers/ }));
    expect(location()).toBe('/storage?tab=layers');
    expect(screen.getByRole('status', { name: 'Selected storage kind' }).textContent).toBe('1 container layers · sorted by size');
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
    expect(region.textContent).toBe('1 container layers · sorted by size');
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
