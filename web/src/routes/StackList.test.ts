import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { writable } from 'svelte/store';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Change, Operation, StackSummary } from '$lib/api/types';
import { refreshRunningOps, startStacks } from '$lib/stores/stacks';
import { installFakeFetch, type FakeFetch } from '../vitest-setup';
import StackList from './StackList.svelte';

const stack = (name: string, over: Partial<StackSummary> = {}): StackSummary => ({
  name,
  path: `/srv/stacks/${name}/compose.yaml`,
  project: `proj-${name}`,
  origin: 'managed',
  status: 'running',
  serviceCount: 3,
  running: 2,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
  ...over,
});
const op = (name: string): Operation => ({ id: 'op1', stack: name, action: 'up', state: 'running', startedAt: '2026-10-14T10:00:00Z' });

let fake: FakeFetch;
let stop: (() => void) | undefined;

async function mount(items: StackSummary[], ops: Operation[] = []) {
  fake = installFakeFetch({
    'GET /api/stacks': { body: items },
    'GET /api/operations?state=running': { body: ops },
  });
  stop = startStacks({
    list: () => fetch('/api/stacks').then((r) => r.json()),
    changes: writable<(Change & { resync?: true }) | null>(null),
  });
  const view = render(StackList);
  await waitFor(() => expect(screen.queryByText('Loading')).toBeNull());
  return view;
}

afterEach(() => {
  stop?.();
  stop = undefined;
  fake?.restore();
});

describe('StackList', () => {
  it('renders rows sorted by name with badge, counts, origin, project and linked name', async () => {
    await mount([stack('zeta', { status: 'stopped', running: 0 }), stack('alpha')]);
    const rows = screen.getAllByRole('row').slice(1);
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByRole('link', { name: 'alpha' }).getAttribute('href')).toBe('/stacks/alpha');
    expect(within(rows[0]).getByText('running')).toBeTruthy();
    expect(within(rows[0]).getByText('2/3')).toBeTruthy();
    expect(within(rows[0]).getByText('managed')).toBeTruthy();
    expect(within(rows[0]).getByText('proj-alpha')).toBeTruthy();
    expect(within(rows[0]).getByText(/compose\.yaml/).getAttribute('title')).toBe('/srv/stacks/alpha/compose.yaml');
    expect(within(rows[1]).getByText('stopped')).toBeTruthy();
    expect(screen.getByText(/2 stacks/)).toBeTruthy();
  });

  it('shows the empty state with create and import links', async () => {
    await mount([]);
    expect(screen.getByRole('link', { name: /create/i }).getAttribute('href')).toBe('/stacks/new');
    expect(screen.getByRole('link', { name: /import/i }).getAttribute('href')).toBe('/stacks/import');
  });

  it('search filters by name or project', async () => {
    await mount([stack('alpha'), stack('beta', { project: 'shared-db' })]);
    const user = userEvent.setup();
    await user.type(screen.getByRole('searchbox', { name: /search/i }), 'shared');
    expect(screen.queryByRole('link', { name: 'alpha' })).toBeNull();
    expect(screen.getByRole('link', { name: 'beta' })).toBeTruthy();
    await user.clear(screen.getByRole('searchbox', { name: /search/i }));
    await user.type(screen.getByRole('searchbox', { name: /search/i }), 'ALP');
    expect(screen.getByRole('link', { name: 'alpha' })).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'beta' })).toBeNull();
  });

  it('shows a running-operation spinner only for stacks with a running operation', async () => {
    await mount([stack('alpha'), stack('beta')], [op('beta')]);
    const rows = screen.getAllByRole('row').slice(1);
    await waitFor(() => expect(within(rows[1]).getByText('Operation running on beta')).toBeTruthy());
    expect(within(rows[0]).queryByText(/Operation running/)).toBeNull();
  });

  it('flags unavailable stacks with a warning that links to the detail page', async () => {
    await mount([stack('broken', { status: 'unavailable' })]);
    expect(screen.getByText('unavailable')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'broken' }).getAttribute('href')).toBe('/stacks/broken');
  });

  it('notes degraded status when every stack is unknown', async () => {
    await mount([stack('a', { status: 'unknown' }), stack('b', { status: 'unknown' })]);
    expect(screen.getByText(/status is unavailable/i)).toBeTruthy();
  });

  it('does not show the degraded note when some stacks have a known status', async () => {
    await mount([stack('a', { status: 'unknown' }), stack('b')]);
    expect(screen.queryByText(/status is unavailable/i)).toBeNull();
  });

  it('encodes names with spaces and # in links', async () => {
    await mount([stack('my stack#1')]);
    expect(screen.getByRole('link', { name: 'my stack#1' }).getAttribute('href')).toBe('/stacks/my%20stack%231');
  });

  it('names the table and keeps table roles', async () => {
    await mount([stack('alpha')]);
    expect(screen.getByRole('table', { name: 'Stacks' })).toBeTruthy();
    expect(screen.getAllByRole('columnheader').length).toBe(6);
  });

  it('shows no-match text only for a non-empty query', async () => {
    await mount([stack('alpha')]);
    expect(screen.queryByText(/No stacks match/)).toBeNull();
    await userEvent.setup().type(screen.getByRole('searchbox'), 'zzz');
    expect(screen.getByText(/No stacks match/)).toBeTruthy();
  });

  it('rechecks running operations with backoff until none run, and on demand', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      let running = [op('alpha')];
      fake = installFakeFetch({
        'GET /api/stacks': { body: [stack('alpha')] },
        'GET /api/operations?state=running': () => ({ body: running }),
      });
      stop = startStacks({
        list: () => fetch('/api/stacks').then((r) => r.json()),
        changes: writable<(Change & { resync?: true }) | null>(null),
      });
      render(StackList);
      await waitFor(() => expect(screen.getByText('Operation running on alpha')).toBeTruthy());
      const opsCalls = () => fake.calls.filter((c) => c.path.startsWith('/api/operations')).length;
      const before = opsCalls();
      running = [];
      await vi.advanceTimersByTimeAsync(2100);
      await waitFor(() => expect(screen.queryByText(/Operation running/)).toBeNull());
      const after = opsCalls();
      expect(after).toBeGreaterThan(before);
      await vi.advanceTimersByTimeAsync(30000);
      expect(opsCalls()).toBe(after); // stopped polling once nothing runs
      running = [op('alpha')];
      refreshRunningOps();
      await waitFor(() => expect(screen.getByText('Operation running on alpha')).toBeTruthy());
    } finally {
      vi.useRealTimers();
    }
  });

  it('first-load failure shows only the banner and a Retry that refetches', async () => {
    let fail = true;
    fake = installFakeFetch({
      'GET /api/stacks': () => (fail ? { status: 500, body: { error: 'boom' } } : { body: [stack('alpha')] }),
      'GET /api/operations?state=running': { body: [] },
    });
    stop = startStacks({
      list: async () => {
        const r = await fetch('/api/stacks');
        if (!r.ok) throw new Error('boom');
        return r.json();
      },
      changes: writable<(Change & { resync?: true }) | null>(null),
    });
    render(StackList);
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'));
    expect(screen.queryByRole('table')).toBeNull();
    expect(screen.queryByText(/No stacks match/)).toBeNull();
    fail = false;
    await userEvent.setup().click(screen.getByRole('button', { name: /retry/i }));
    await waitFor(() => expect(screen.getByRole('link', { name: 'alpha' })).toBeTruthy());
  });

  it('shows a busy Retry while the refetch is in flight', async () => {
    let release: (() => void) | undefined;
    let fail = true;
    fake = installFakeFetch({
      'GET /api/stacks': async () => {
        if (fail) return { status: 500, body: { error: 'boom' } };
        await new Promise<void>((r) => (release = r));
        return { body: [stack('alpha')] };
      },
      'GET /api/operations?state=running': { body: [] },
    });
    stop = startStacks({
      list: async () => {
        const r = await fetch('/api/stacks');
        if (!r.ok) throw new Error('boom');
        return r.json();
      },
      changes: writable<(Change & { resync?: true }) | null>(null),
    });
    render(StackList);
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'));
    fail = false;
    await userEvent.setup().click(screen.getByRole('button', { name: /retry/i }));
    await waitFor(() => expect(screen.getByRole('button', { name: /retry/i }).getAttribute('aria-busy')).toBe('true'));
    release?.();
    await waitFor(() => expect(screen.getByRole('link', { name: 'alpha' })).toBeTruthy());
  });
});
