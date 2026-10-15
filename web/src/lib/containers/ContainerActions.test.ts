import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { get } from 'svelte/store';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ContainerRow } from '$lib/api/types';
import * as liveModule from '$lib/stores/live';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type FakeResponse } from '../../vitest-setup';
import ContainerActions from './ContainerActions.svelte';

const liveState = vi.hoisted(() => ({ value: { connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' } as Record<string, unknown> }));
vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('svelte/store');
  const store = writable(liveState.value);
  return {
    ...(await orig<typeof import('$lib/stores/live')>()),
    live: { subscribe: store.subscribe },
    refreshStatus: vi.fn(),
    __set: store.set,
  };
});
const setLiveState = (v: Record<string, unknown>) => (liveModule as unknown as { __set: (v: unknown) => void }).__set(v);

const row = (over: Partial<ContainerRow> = {}): ContainerRow => ({
  id: 'abc123def456',
  name: 'redis',
  image: 'redis:7',
  state: 'running',
  status: 'Up 1 hour',
  health: 'none',
  exitCode: null,
  createdAt: '2026-01-01T00:00:00Z',
  project: '',
  service: '',
  stack: '',
  managed: 'none',
  ports: [],
  networks: {},
  cpu: null,
  mem: null,
  memLimit: null,
  restarts24h: 0,
  ...over,
});

let fake: FakeFetch;
function mount(r: ContainerRow, route: FakeResponse | (() => FakeResponse | Promise<FakeResponse>) = { body: row() }, props: Record<string, unknown> = {}) {
  fake = installFakeFetch({ 'POST /api/containers/abc123def456/stop': route, 'POST /api/containers/abc123def456/start': route, 'POST /api/containers/abc123def456/restart': route });
  render(ContainerActions, { row: r, ...props });
  return userEvent.setup();
}

afterEach(() => {
  toasts.reset();
  setLiveState(liveState.value);
  fake?.restore();
});

describe('ContainerActions', () => {
  it('offers only a stack link for a container in a registered stack', () => {
    mount(row({ managed: 'stack', stack: 'shop' }));
    expect(screen.getByRole('link', { name: /Manage in stack/ }).getAttribute('href')).toBe('/stacks/shop');
    expect(screen.queryByRole('button')).toBeNull();
  });

  it('encodes the stack name in the link', () => {
    mount(row({ managed: 'stack', stack: 'my stack' }));
    expect(screen.getByRole('link', { name: /Manage in stack/ }).getAttribute('href')).toBe('/stacks/my%20stack');
  });

  it('shows Stop and Restart for a running container', () => {
    mount(row());
    expect(screen.getByRole('button', { name: 'Stop redis' }).getAttribute('title')).toBe('Stop redis');
    expect(screen.getByRole('button', { name: 'Restart redis' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Start redis' })).toBeNull();
  });

  it('shows Start and Restart for a stopped container', () => {
    mount(row({ state: 'exited' }), undefined);
    expect(screen.getByRole('button', { name: 'Start redis' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Restart redis' })).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Stop redis' })).toBeNull();
  });

  it('tags each action button so hover colours follow the action, not the position', () => {
    mount(row());
    expect(screen.getByRole('button', { name: 'Stop redis' }).getAttribute('data-action')).toBe('stop');
    expect(screen.getByRole('button', { name: 'Restart redis' }).getAttribute('data-action')).toBe('restart');
  });

  it('shows text labels at the md size', () => {
    mount(row(), undefined, { size: 'md' });
    expect(screen.getByRole('button', { name: 'Stop redis' }).textContent).toContain('Stop');
  });

  it('starts without confirmation, toasts and reports the fresh row', async () => {
    const fresh = row({ state: 'running' });
    const onchanged = vi.fn();
    const user = mount(row({ state: 'exited' }), { body: fresh }, { onchanged });
    await user.click(screen.getByRole('button', { name: 'Start redis' }));
    await waitFor(() => expect(onchanged).toHaveBeenCalledWith(fresh));
    expect(fake.calls.map((c) => `${c.method} ${c.path}`)).toEqual(['POST /api/containers/abc123def456/start']);
    expect(get(toasts).map((t) => t.message)).toEqual(['redis started']);
  });

  it('on 204 (the action removed the container) still toasts success and reports no row', async () => {
    const onchanged = vi.fn();
    const user = mount(row(), { status: 204 }, { onchanged });
    await user.click(screen.getByRole('button', { name: 'Stop redis' }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Stop' }));
    await waitFor(() => expect(onchanged).toHaveBeenCalledWith(undefined));
    expect(get(toasts).map((t) => t.message)).toEqual(['redis stopped']);
  });

  it.each([
    ['Stop', 'stopped'],
    ['Restart', 'restarted'],
  ])('asks before %s, naming the container, and cancel sends nothing', async (label, _done) => {
    const user = mount(row());
    await user.click(screen.getByRole('button', { name: `${label} redis` }));
    const dialog = screen.getByRole('alertdialog');
    expect(dialog.textContent).toContain('redis');
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(fake.calls).toHaveLength(0);
  });

  it.each([
    ['Stop', 'stop', 'redis stopped'],
    ['Restart', 'restart', 'redis restarted'],
  ])('confirming %s sends the request and toasts', async (label, path, message) => {
    const onchanged = vi.fn();
    const user = mount(row(), { body: row() }, { onchanged });
    await user.click(screen.getByRole('button', { name: `${label} redis` }));
    await user.click(within_dialog(label));
    await waitFor(() => expect(onchanged).toHaveBeenCalledTimes(1));
    expect(fake.calls[0].path).toBe(`/api/containers/abc123def456/${path}`);
    expect(get(toasts).map((t) => t.message)).toEqual([message]);
  });

  it('is soft-disabled with the reason while Docker is unreachable', async () => {
    setLiveState({ ...liveState.value, docker: 'unavailable' });
    const user = mount(row());
    const stop = screen.getByRole('button', { name: 'Stop redis' });
    expect(stop.getAttribute('aria-disabled')).toBe('true');
    expect(stop.getAttribute('title')).toBe('Docker is unreachable');
    await user.click(stop);
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(fake.calls).toHaveLength(0);
  });

  it('is soft-disabled when the events stream is disconnected', () => {
    setLiveState({ ...liveState.value, events: 'disconnected' });
    mount(row());
    expect(screen.getByRole('button', { name: 'Restart redis' }).getAttribute('title')).toBe('Docker is unreachable');
  });

  it('sends one request on a double click and disables the row while in flight', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const user = mount(row({ state: 'exited' }), async () => {
      await gate;
      return { body: row() };
    });
    const start = screen.getByRole('button', { name: 'Start redis' });
    await user.dblClick(start);
    await waitFor(() => expect(fake.calls).toHaveLength(1));
    expect(screen.getByRole('button', { name: 'Restart redis' }).getAttribute('aria-disabled')).toBe('true');
    release();
    await waitFor(() => expect(get(toasts)).toHaveLength(1));
    expect(fake.calls).toHaveLength(1);
  });

  it('toasts the server message on a 409 as a warning and does not report a change', async () => {
    const onchanged = vi.fn();
    const user = mount(row({ state: 'exited' }), { status: 409, body: { error: 'redis belongs to stack "shop"; manage it there' } }, { onchanged });
    await user.click(screen.getByRole('button', { name: 'Start redis' }));
    await waitFor(() => expect(get(toasts)).toHaveLength(1));
    expect(get(toasts)[0]).toMatchObject({ kind: 'warning', message: 'redis belongs to stack "shop"; manage it there' });
    expect(onchanged).not.toHaveBeenCalled();
  });

  it('toasts Docker\'s message on a 502 as a danger and re-enables the row', async () => {
    const user = mount(row({ state: 'exited' }), { status: 502, body: { error: 'port is already allocated' } });
    await user.click(screen.getByRole('button', { name: 'Start redis' }));
    await waitFor(() => expect(get(toasts)).toHaveLength(1));
    expect(get(toasts)[0]).toMatchObject({ kind: 'danger', message: 'port is already allocated' });
    await waitFor(() => expect(screen.getByRole('button', { name: 'Start redis' }).getAttribute('aria-disabled')).toBeNull());
  });
});

function within_dialog(label: string) {
  const dialog = screen.getByRole('alertdialog');
  const buttons = Array.from(dialog.querySelectorAll('button'));
  return buttons.find((b) => b.textContent?.trim() === label) as HTMLElement;
}
