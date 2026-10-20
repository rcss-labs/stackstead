import { act, cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { consumer, declaration, image, volume } from '$lib/storage/fixtures';
import { operationsRefresh } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream } from '../../vitest-setup';
import { findRoute } from '../routes';
import { location, mountAt } from './storageTestKit';

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
let stream: FakeStream | undefined;

afterEach(() => {
  cleanup();
  stream?.restore();
  stream = undefined;
  fake?.restore();
  toasts.reset();
  vi.useRealTimers();
});

const fullId = 'sha256:' + 'a'.repeat(64);
const imageDetail = (over = {}) => ({ ...image(), architecture: 'arm64', os: 'linux', layers: 1, labels: {}, ...over });
const volumeDetail = (over = {}) => ({ ...volume({ name: 'free', protection: null, declaredBy: [] }), mountpoint: '/data', ...over });
const op = { id: 'op1', stack: '', action: 'storage-cleanup', state: 'running', startedAt: '2026-01-01T00:00:00Z' };
const planFor = (kind: 'image' | 'volume') => ({
  id: 'p_1',
  kind,
  expiresAt: new Date(Date.now() + 300000).toISOString(),
  createdAt: new Date().toISOString(),
  items: [{ kind, id: 'free', ref: 'free', bytes: 0 }],
  protected: [],
  warnings: [],
  reclaim: { min: 0, max: 0, exact: true },
  confirmPhrase: 'free',
});

describe('Storage detail screens', () => {
  it('renders only safe image details, consumers, declarations and escaped labels', async () => {
    fake = installFakeFetch({
      'GET /api/storage/images/aaaaaaaaaaaa': {
        body: {
          ...imageDetail({ containers: [consumer], declaredBy: [declaration], digests: ['redis@sha256:123'], size: 1024, layers: 7, labels: { hello: '<b>world</b>' } }),
          env: ['SECRET=do-not-render'],
          cmd: 'unsafe-cmd',
        },
      },
    });
    mountAt('/storage/images/aaaaaaaaaaaa');
    await screen.findByRole('heading', { name: 'redis:7', level: 1 });
    for (const text of ['redis@sha256:123', 'arm64 / linux', '7', '1.0 KiB', '<b>world</b>']) expect(screen.getByText(text)).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Copy image ID' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'db' }).getAttribute('href')).toBe('/containers/db');
    expect(screen.getByRole('link', { name: 'media' }).getAttribute('href')).toBe('/stacks/media');
    expect(screen.getByRole('button', { name: 'Delete' }).title).toBe('used by 1 container');
    expect(document.body.textContent).not.toContain('do-not-render');
    expect(document.body.textContent).not.toContain('unsafe-cmd');
    expect(document.querySelector('b')).toBeNull();
  });

  it('renders volume fields and protection note without driver options', async () => {
    fake = installFakeFetch({ 'GET /api/storage/volumes/media_db': { body: { ...volume(), mountpoint: '/var/lib/docker/volumes/media_db', options: { password: 'hidden-secret' } } } });
    mountAt('/storage/volumes/media_db');
    await screen.findByRole('heading', { name: 'media_db', level: 1 });
    expect(screen.getByText('/var/lib/docker/volumes/media_db')).toBeTruthy();
    expect(screen.getAllByText('local')).toHaveLength(2);
    expect(screen.getAllByText(/declared by stack/).length).toBeGreaterThan(0);
    expect(screen.getByRole('button', { name: 'Delete' }).getAttribute('aria-disabled')).toBe('true');
    expect(document.body.textContent).not.toContain('hidden-secret');
  });

  it.each(['image', 'volume'])('unknown %s has not found and matching back link', async (kind) => {
    fake = installFakeFetch({});
    mountAt(kind === 'image' ? '/storage/images/aaaaaaaaaaaa' : '/storage/volumes/missing');
    expect(await screen.findByText(new RegExp(`${kind} not found`, 'i'))).toBeTruthy();
    expect(screen.getByRole('link', { name: kind === 'image' ? 'Back to images' : 'Back to volumes' }).getAttribute('href')).toBe(`/storage?tab=${kind}s`);
  });

  it('single volume deletion uses named plan and returns to volumes after operation ends', async () => {
    fake = installFakeFetch({
      'GET /api/storage/volumes/free': { body: volumeDetail() },
      'POST /api/storage/plans': { body: planFor('volume') },
      'POST /api/storage/plans/p_1/confirm': { body: op },
      'GET /api/operations/op1': { body: { ...op, lines: [] } },
    });
    stream = installFakeStream({ '/api/operations/op1/stream': [] });
    mountAt('/storage/volumes/free');
    await screen.findByRole('heading', { name: 'free', level: 1 });
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(await within(dialog).findByRole('textbox'), { target: { value: 'free' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(stream!.requests).toHaveLength(1));
    expect(fake.calls.find((c) => c.path === '/api/storage/plans')?.body).toEqual({ kind: 'volume', name: 'free' });
    await act(async () => stream!.push('/api/operations/op1/stream', `event: done\ndata: ${JSON.stringify({ ...op, state: 'succeeded' })}\n\n`));
    await waitFor(() => expect(location()).toBe('/storage?tab=volumes'));
  });

  it.each([
    { kind: 'image' as const, status: 404 },
    { kind: 'image' as const, status: 503 },
    { kind: 'volume' as const, status: 404 },
    { kind: 'volume' as const, status: 503 },
  ])('keeps $kind cleanup alive after a $status detail refresh until terminal output', async ({ kind, status }) => {
    const plural = `${kind}s`;
    const target = kind === 'image' ? 'aaaaaaaaaaaa' : 'free';
    let detail: FakeResponse = { body: kind === 'image' ? imageDetail() : volumeDetail() };
    fake = installFakeFetch({
      [`GET /api/storage/${plural}/${target}`]: () => detail,
      'POST /api/storage/plans': { body: planFor(kind) },
      'POST /api/storage/plans/p_1/confirm': { body: op },
      'GET /api/operations/op1': { body: { ...op, lines: [] } },
    });
    stream = installFakeStream({ '/api/operations/op1/stream': [] });
    mountAt(`/storage/${plural}/${target}`);
    await screen.findByRole('heading', { level: 1, name: kind === 'image' ? 'redis:7' : 'free' });
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(await within(dialog).findByRole('textbox'), { target: { value: 'free' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
    const output = await screen.findByRole('log');
    await waitFor(() => expect(stream!.requests).toHaveLength(1));
    await act(async () => stream!.push('/api/operations/op1/stream', 'event: line\ndata: "deleted previewed item"\n\n'));
    await screen.findByText('deleted previewed item');
    const before = get(operationsRefresh);
    detail = { status, body: { error: status === 404 ? 'not found' : 'Temporary Docker outage' } };
    await act(async () => void document.dispatchEvent(new Event('visibilitychange')));
    if (status === 404) await screen.findByRole('heading', { level: 1, name: kind === 'image' ? 'Image not found' : 'Volume not found' });
    else await screen.findByRole('alert');
    expect(screen.queryByRole('dialog'), 'active cleanup must survive refreshed detail data').not.toBeNull();
    expect(screen.getByRole('log')).toBe(output);
    expect(output.textContent).toContain('deleted previewed item');
    expect(stream.requests).toHaveLength(1);
    expect(stream.requests[0].signal?.aborted).toBe(false);
    expect(fake.calls.find((c) => c.path === '/api/storage/plans')?.body).toEqual(kind === 'image' ? { kind, ref: fullId } : { kind, name: 'free' });
    expect(get(toasts)).toHaveLength(0);
    expect(get(operationsRefresh)).toBe(before);
    const done = `event: done\ndata: ${JSON.stringify({ ...op, state: 'succeeded', exitCode: 0 })}\n\n`;
    await act(async () => stream!.push('/api/operations/op1/stream', done + done));
    await waitFor(() => expect(location()).toBe(`/storage?tab=${plural}`));
    expect(get(toasts)).toHaveLength(1);
    expect(get(toasts)[0].message).toBe('Storage cleanup succeeded');
    expect(get(operationsRefresh)).toBe(before + 1);
  });

  it('navigating from one image to another reloads the page', async () => {
    fake = installFakeFetch({
      'GET /api/storage/images/aaaaaaaaaaaa': { body: imageDetail() },
      'GET /api/storage/images/bbbbbbbbbbbb': { body: imageDetail({ id: 'sha256:' + 'b'.repeat(64), tags: ['other:1'] }) },
    });
    const view = mountAt('/storage/images/aaaaaaaaaaaa');
    await screen.findByRole('heading', { level: 1, name: 'redis:7' });
    view.goTo('/storage/images/bbbbbbbbbbbb');
    await screen.findByRole('heading', { level: 1, name: 'other:1' });
    expect(screen.queryByText('redis:7')).toBeNull();
    expect(fake.calls.filter((c) => c.path.startsWith('/api/storage/images/'))).toHaveLength(2);
  });

  it('navigating from one volume to another reloads the page', async () => {
    fake = installFakeFetch({
      'GET /api/storage/volumes/media_db': { body: { ...volume(), mountpoint: '/m' } },
      'GET /api/storage/volumes/free': { body: volumeDetail() },
    });
    const view = mountAt('/storage/volumes/media_db');
    await screen.findByRole('heading', { level: 1, name: 'media_db' });
    view.goTo('/storage/volumes/free');
    await screen.findByRole('heading', { level: 1, name: 'free' });
    expect(screen.queryByText('media_db')).toBeNull();
    expect(fake.calls.filter((c) => c.path.startsWith('/api/storage/volumes/'))).toHaveLength(2);
  });

  it('the h1 is present while the request is pending', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    fake = installFakeFetch({
      'GET /api/storage/images/aaaaaaaaaaaa': async () => {
        await gate;
        return { body: imageDetail() };
      },
    });
    mountAt('/storage/images/aaaaaaaaaaaa');
    expect(await screen.findByRole('heading', { level: 1, name: 'Image details' })).toBeTruthy();
    expect(screen.getByText('Loading image…')).toBeTruthy();
    release();
    await screen.findByRole('heading', { level: 1, name: 'redis:7' });
  });

  it('the route table titles the detail routes Storage', () => {
    expect(findRoute('/storage/images/x')?.title).toBe('Storage');
    expect(findRoute('/storage/volumes/x')?.title).toBe('Storage');
  });
});
