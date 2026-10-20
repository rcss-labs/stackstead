import { act, cleanup, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ContainerRow } from '$lib/api/types';
import { get } from '$lib/store';
import * as liveModule from '$lib/stores/live';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream } from '../../vitest-setup';
import { mountAt, row } from './containersTestKit';

const liveState = vi.hoisted(() => ({ value: { connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' } as Record<string, unknown> }));
vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const store = writable(liveState.value);
  const changes = writable<{ project: string; seq: number; resync?: true } | null>(null);
  const pushedChanges = changes;
  return {
    ...(await orig<typeof import('$lib/stores/live')>()),
    live: { subscribe: store.subscribe },
    changes: { subscribe: changes.subscribe },
    refreshStatus: vi.fn(),
    __set: store.set,
    __changes: pushedChanges,
  };
});
const setLiveState = (v: Record<string, unknown>) => act(() => (liveModule as unknown as { __set: (v: unknown) => void }).__set(v));

const pushChange = (seq: number) => act(() => (liveModule as unknown as { __changes: { set: (v: unknown) => void } }).__changes.set({ project: '', seq, resync: true }));

const stubWidth = () => vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }));
const letStoresRelease = () => new Promise((r) => setTimeout(r, 5));

let fetchFake: FakeFetch;
let stream: FakeStream;
function mount(list: FakeResponse, extra: Record<string, FakeResponse> = {}) {
  const rows = list.body as ContainerRow[];
  fetchFake = installFakeFetch({ 'GET /api/containers': list, ...extra });
  stream = installFakeStream(Object.fromEntries(rows.map((r) => [`/api/containers/${r.id}/logs`, []])));
  mountAt('/containers');
  return userEvent.setup();
}
const bodyRows = () => screen.getAllByRole('row').filter((r) => r.hasAttribute('data-container'));
const rowOf = (name: string) => bodyRows().find((r) => r.getAttribute('data-container') === name)!;
const focusedName = () => bodyRows().find((r) => r.hasAttribute('data-focused'))?.getAttribute('data-container');
const posts = () => fetchFake.calls.filter((c) => c.method === 'POST');

beforeEach(stubWidth);
afterEach(async () => {
  cleanup();
  toasts.reset();
  stream?.restore();
  fetchFake?.restore();
  setLiveState(liveState.value);
  vi.unstubAllGlobals();
  await letStoresRelease();
});

const abc = () => ({ body: [row('a'), row('b'), row('c')] });

describe('Container keys', () => {
  it('j and k move the focused row and clamp at both ends', async () => {
    const user = mount(abc());
    await screen.findByRole('link', { name: 'a' });
    expect(focusedName()).toBe('a');
    await user.keyboard('j');
    expect(focusedName()).toBe('b');
    expect(document.activeElement).toBe(rowOf('b'));
    await user.keyboard('jj');
    expect(focusedName()).toBe('c');
    await user.keyboard('kkk');
    expect(focusedName()).toBe('a');
    expect(document.activeElement).toBe(rowOf('a'));
  });

  it('Enter toggles the peek of the focused row and Escape closes it', async () => {
    const user = mount(abc());
    await screen.findByRole('link', { name: 'a' });
    await user.keyboard('j');
    await user.keyboard('{Enter}');
    await screen.findByRole('region', { name: 'Logs of b' });
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    expect(stream.requests[0].path).toContain('/api/containers/b00000000000/logs');
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('region', { name: /^Logs of/ })).toBeNull();
    expect(stream.requests[0].signal!.aborted).toBe(true);
  });

  it('r asks to restart the focused container through the shared dialog', async () => {
    const user = mount(abc(), { 'POST /api/containers/a00000000000/restart': { body: row('a') } });
    await screen.findByRole('link', { name: 'a' });
    await user.keyboard('r');
    const dialog = await screen.findByRole('alertdialog');
    expect(dialog.textContent).toContain('a');
    await user.click(within(dialog).getByRole('button', { name: 'Restart' }));
    await waitFor(() => expect(posts().map((c) => c.path)).toEqual(['/api/containers/a00000000000/restart']));
    await waitFor(() => expect(get(toasts).map((t) => t.message)).toEqual(['a restarted']));
  });

  it('r does nothing for a stack-managed container or while Docker is unreachable', async () => {
    const user = mount({ body: [row('a', { managed: 'stack', stack: 's' }), row('b')] });
    await screen.findByRole('link', { name: 'a' });
    await user.keyboard('r');
    expect(screen.queryByRole('alertdialog')).toBeNull();
    await user.keyboard('j');
    await user.keyboard('r');
    await screen.findByRole('alertdialog');
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    setLiveState({ ...liveState.value, docker: 'unavailable' });
    await user.keyboard('r');
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(posts()).toHaveLength(0);
  });

  it('keys are ignored while typing in the search box and inside a dialog', async () => {
    const user = mount(abc());
    await screen.findByRole('link', { name: 'a' });
    const search = screen.getByRole('searchbox', { name: 'Search containers' });
    await user.click(search);
    await user.keyboard('j');
    expect((search as HTMLInputElement).value).toBe('j');
    await user.clear(search);
    expect(focusedName()).toBe('a');
    act(() => rowOf('a').focus());
    await user.keyboard('{Enter}');
    await screen.findByRole('region', { name: 'Logs of a' });
    await user.keyboard('r');
    await screen.findByRole('alertdialog');
    await user.keyboard('j');
    expect(focusedName()).toBe('a');
    expect(screen.getByRole('alertdialog')).toBeTruthy();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    expect(screen.getByRole('region', { name: 'Logs of a' })).toBeTruthy();
  });

  it('Escape with the dialog open and focus outside it closes only the dialog', async () => {
    const user = mount(abc());
    await screen.findByRole('link', { name: 'a' });
    act(() => rowOf('a').focus());
    await user.keyboard('{Enter}');
    await screen.findByRole('region', { name: 'Logs of a' });
    await user.keyboard('r');
    await screen.findByRole('alertdialog');
    act(() => (document.activeElement as HTMLElement).blur());
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull());
    expect(screen.getByRole('region', { name: 'Logs of a' })).toBeTruthy();
  });

  it('keys typed in the focused log region belong to the log', async () => {
    const user = mount(abc());
    await screen.findByRole('link', { name: 'a' });
    act(() => rowOf('a').focus());
    await user.keyboard('{Enter}');
    const log = (await screen.findByRole('region', { name: 'Logs of a' })).querySelector('[role="log"]') as HTMLElement;
    act(() => log.focus());
    await user.keyboard('j{Enter}');
    expect(document.activeElement).toBe(log);
    expect(focusedName()).toBe('a');
    expect(screen.getByRole('region', { name: 'Logs of a' })).toBeTruthy();
  });

  it('keeps the ring and the actions on the same container when a refetch re-sorts the list above it', async () => {
    let body = [row('a'), row('b'), row('c')];
    const user = mount({ body }, {});
    fetchFake.restore();
    fetchFake = installFakeFetch({
      'GET /api/containers': () => ({ body }),
      'POST /api/containers/b00000000000/restart': { body: row('b') },
    });
    await screen.findByRole('link', { name: 'a' });
    await user.keyboard('j');
    expect(focusedName()).toBe('b');
    body = [row('a'), row('b'), row('c'), row('p', { state: 'exited', exitCode: 1 })];
    pushChange(1);
    await waitFor(() => expect(bodyRows().map((r) => r.getAttribute('data-container'))[0]).toBe('p'));
    expect(focusedName()).toBe('b');
    await user.keyboard('{Enter}');
    await screen.findByRole('region', { name: 'Logs of b' });
    await user.keyboard('{Escape}');
    await user.keyboard('r');
    const dialog = await screen.findByRole('alertdialog');
    expect(dialog.textContent).toContain('b');
    expect(dialog.textContent).not.toContain('Restart p');
  });

  it('falls back to a clamped index when the focused container disappears', async () => {
    let body = [row('a'), row('b'), row('c')];
    const user = mount({ body });
    fetchFake.restore();
    fetchFake = installFakeFetch({ 'GET /api/containers': () => ({ body }) });
    await screen.findByRole('link', { name: 'a' });
    await user.keyboard('jj');
    expect(focusedName()).toBe('c');
    body = [row('a'), row('b')];
    pushChange(1);
    await waitFor(() => expect(bodyRows()).toHaveLength(2));
    expect(focusedName()).toBe('b');
  });

  it('Escape closes the peek even from inside its log region, while other keys stay with the log', async () => {
    const user = mount(abc());
    await screen.findByRole('link', { name: 'a' });
    act(() => rowOf('a').focus());
    await user.keyboard('{Enter}');
    const log = (await screen.findByRole('region', { name: 'Logs of a' })).querySelector('[role="log"]') as HTMLElement;
    act(() => log.focus());
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('region', { name: /^Logs of/ })).toBeNull();
  });

  it('Escape from inside the log region closes the peek and returns focus to its row', async () => {
    const user = mount(abc());
    await screen.findByRole('link', { name: 'a' });
    act(() => rowOf('a').focus());
    await user.keyboard('{Enter}');
    const log = (await screen.findByRole('region', { name: 'Logs of a' })).querySelector('[role="log"]') as HTMLElement;
    act(() => log.focus());
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('log')).toBeNull();
    expect(document.activeElement).toBe(rowOf('a'));
  });

  it('arrow keys move the row focus only from inside the table; j and k work anywhere', async () => {
    const user = mount(abc());
    await screen.findByRole('link', { name: 'a' });
    const chip = screen.getByRole('button', { name: /^Running/ });
    act(() => chip.focus());
    await user.keyboard('{ArrowDown}');
    expect(focusedName()).toBe('a');
    await user.keyboard('j');
    expect(focusedName()).toBe('b');
    act(() => rowOf('b').focus());
    await user.keyboard('{ArrowDown}');
    expect(focusedName()).toBe('c');
    await user.keyboard('{ArrowUp}');
    expect(focusedName()).toBe('b');
  });

  it('Enter on the name link and on the toggle button is left to the element', async () => {
    const user = mount(abc());
    await screen.findByRole('link', { name: 'a' });
    screen.getByRole('link', { name: 'a' }).focus();
    await user.keyboard('{Enter}');
    expect(stream.requests).toHaveLength(0);
    cleanup();
    await letStoresRelease();
    mountAt('/containers');
    await screen.findByRole('link', { name: 'a' });
    screen.getByRole('button', { name: 'Peek logs for a' }).focus();
    await user.keyboard('{Enter}');
    await screen.findByRole('region', { name: 'Logs of a' });
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    expect(screen.getAllByRole('region', { name: /^Logs of/ })).toHaveLength(1);
  });

  it('modifier keys are ignored', async () => {
    const user = mount(abc());
    await screen.findByRole('link', { name: 'a' });
    await user.keyboard('{Meta>}j{/Meta}');
    expect(focusedName()).toBe('a');
    await user.keyboard('{Control>}j{/Control}');
    expect(focusedName()).toBe('a');
    await user.keyboard('{Alt>}j{/Alt}');
    expect(focusedName()).toBe('a');
    await user.keyboard('j');
    expect(focusedName()).toBe('b');
  });

  it('a chip change resets the focus to the first visible row', async () => {
    const user = mount({ body: [row('a'), row('b'), row('c', { state: 'exited', exitCode: 0 })] });
    await screen.findByRole('link', { name: 'a' });
    await user.keyboard('j');
    expect(focusedName()).toBe('b');
    await user.click(screen.getByRole('button', { name: /^Running/ }));
    expect(focusedName()).toBe('a');
  });

  it('keys are harmless on an empty list', async () => {
    const user = mount({ body: [] });
    await screen.findByText('No containers on this host.');
    await user.keyboard('jk{Enter}r{Escape}');
    expect(stream.requests).toHaveLength(0);
    expect(posts()).toHaveLength(0);
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });
});
