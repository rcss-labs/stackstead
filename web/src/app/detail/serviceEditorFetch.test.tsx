import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import type { Library, LibraryServer } from '$lib/api/imageTypes';
import type { ServiceDetail, StackDetail } from '$lib/api/types';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type FakeResponse } from '../../vitest-setup';
import { image, version } from '../images/imagesTestKit';

vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));
vi.mock('$lib/stores/live', async (importActual) => ({ ...(await importActual<object>()), refreshStatus: vi.fn(async () => {}) }));
vi.mock('$lib/stores/stacks', async (importActual) => ({
  ...(await importActual<object>()),
  refreshStacks: vi.fn(async () => {}),
  refreshRunningOps: vi.fn(),
}));

import { ServiceEditor } from './ServiceEditor';

const local: LibraryServer = { id: 'abcdef012345ffff', name: 'Here', slug: '', local: true, status: 'reachable' };
const peer: LibraryServer = { id: 'fedcba987654aaaa', name: 'mini-02', slug: 'mini-02', local: false, status: 'reachable' };
const PEER_REF = 'stackstead.invalid/fedcba987654/web:2';
const PEER_REF_V1 = 'stackstead.invalid/fedcba987654/web:1';

const serviceWith = (ref: string): ServiceDetail => ({ name: 'app', config: { image: ref, limits: {} }, readOnly: [], containers: [] });
const stackOf = (s: ServiceDetail): StackDetail => ({
  name: 'web', path: '/s/web', project: 'web', origin: 'managed', status: 'running', serviceCount: 1, running: 1,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 }, ports: [], hash: 'h1', services: [s], libraryUpdates: 0,
});

let fake: FakeFetch;
const calls = (method: string, path: string) => fake.calls.filter((c) => c.method === method && c.path === path);

function install(owner: LibraryServer, fetchAnswer: FakeResponse = { body: { id: 'op-f1', stack: 'image/fedcba987654/web', action: 'fetch', state: 'running', startedAt: '2026-10-23T10:00:00Z' } }) {
  const lib: Library = { servers: [local, peer], images: [image({ owner })] };
  const detail = { ...image({ owner }), versions: [version({ tag: '2', startedAt: '2026-10-21T10:00:00Z' }), version({ tag: '1', startedAt: '2026-10-20T10:00:00Z' })], suggestedTag: '3', tagHint: '', arch: 'amd64', revision: 1 };
  fake = installFakeFetch({
    'GET /api/images': { body: lib },
    'GET /api/networks': { body: [] },
    'GET /api/stacks/web/compose': { body: { content: 'x', hash: 'h1' } },
    'GET /api/images/web': { body: detail },
    'GET /api/servers/fedcba987654aaaa/api/images/web': { body: detail },
    'GET /api/status': { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
    'GET /api/operations?state=running': { body: [] },
    'POST /api/stacks/web/services/app/preview': (c) => ({ body: { content: 'x', hash: (c.body as { hash: string }).hash } }),
    'PUT /api/stacks/web/services/app': { body: { hash: 'h2' } },
    'POST /api/images/fetch': fetchAnswer,
  });
}

/** Picks the other version of the image (when `pick` is set) and a restart policy so the form is dirty, then saves. */
async function save(pick?: string) {
  const user = userEvent.setup();
  if (pick) await user.click(await screen.findByRole('radio', { name: pick }));
  await user.selectOptions(await screen.findByLabelText('Restart policy'), 'always');
  await user.click(screen.getByRole('button', { name: 'Preview changes' }));
  await user.click(await screen.findByRole('button', { name: 'Save' }));
}

function mount(ref: string) {
  const s = serviceWith(ref);
  render(
    <MemoryRouter>
      <ServiceEditor stack={stackOf(s)} service={s} onRawEditor={vi.fn()} onReload={vi.fn(async () => stackOf(s))} />
    </MemoryRouter>,
  );
}

beforeEach(() => toasts.reset());
afterEach(async () => {
  cleanup();
  fake.restore();
  await new Promise((r) => setTimeout(r, 5));
});

describe('ServiceEditor library fetch', () => {
  it('posts a fetch for a changed peer ref after saving and tracks the operation', async () => {
    install(peer);
    mount(PEER_REF_V1);
    await save('2');
    await waitFor(() => expect(calls('POST', '/api/images/fetch')).toHaveLength(1));
    expect(calls('POST', '/api/images/fetch')[0].body).toEqual({ ref: PEER_REF });
    expect(await screen.findByRole('button', { name: 'Apply' })).toBeTruthy();
    expect(get(toasts).some((t) => t.message.includes('Saved app'))).toBe(true);
    await waitFor(() => expect(get(toasts).some((t) => t.pending && t.message === 'fetch · image web')).toBe(true));
  });

  it('posts nothing when the ref is unchanged', async () => {
    install(peer);
    mount(PEER_REF);
    await save();
    expect(await screen.findByRole('button', { name: 'Apply' })).toBeTruthy();
    expect(calls('POST', '/api/images/fetch')).toHaveLength(0);
  });

  it('tracks nothing when the server answers 204', async () => {
    install(peer, { status: 204 });
    mount(PEER_REF_V1);
    await save('2');
    await waitFor(() => expect(calls('POST', '/api/images/fetch')).toHaveLength(1));
    expect(await screen.findByRole('button', { name: 'Apply' })).toBeTruthy();
    expect(get(toasts).some((t) => t.pending || t.kind === 'danger')).toBe(false);
  });

  it('explains an architecture mismatch without failing the save', async () => {
    install(peer, { status: 409, body: { error: 'wrong arch', code: 'arch_mismatch' } });
    mount(PEER_REF_V1);
    await save('2');
    expect(await screen.findByRole('button', { name: 'Apply' })).toBeTruthy();
    await waitFor(() => expect(get(toasts).some((t) => /different architecture/i.test(t.message))).toBe(true));
  });

  it('reports another fetch failure without failing the save', async () => {
    install(peer, { status: 500, body: { error: 'boom' } });
    mount(PEER_REF_V1);
    await save('2');
    expect(await screen.findByRole('button', { name: 'Apply' })).toBeTruthy();
    await waitFor(() => expect(get(toasts).some((t) => t.kind === 'danger' && t.message.includes('boom'))).toBe(true));
  });
});
