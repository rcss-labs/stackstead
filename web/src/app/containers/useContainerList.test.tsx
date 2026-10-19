import { act, renderHook, waitFor } from '@testing-library/react';
import { StrictMode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as liveModule from '$lib/stores/live';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { row } from './containersTestKit';
import { useContainerList } from './useContainerList';

vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const live = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  const changes = writable<{ project: string; seq: number; resync?: true } | null>(null);
  return { ...(await orig<typeof import('$lib/stores/live')>()), live: { subscribe: live.subscribe }, changes: { subscribe: changes.subscribe }, refreshStatus: vi.fn(), __changes: changes };
});
const pushChange = (c: { project: string; seq: number; resync?: true }) =>
  (liveModule as unknown as { __changes: { set: (v: unknown) => void } }).__changes.set(c);

let fake: FakeFetch;
const listCalls = () => fake.calls.filter((c) => c.method === 'GET' && c.path === '/api/containers').length;

beforeEach(() => {
  fake = installFakeFetch({ 'GET /api/containers': { body: [row('a'), row('b')] } });
});
afterEach(async () => {
  fake.restore();
  await new Promise((r) => setTimeout(r, 5)); // useStore releases its subscriptions on the next task
});

describe('useContainerList', () => {
  it('starts the list on mount, exposes rows and error, and stops it on unmount', async () => {
    const { result, unmount } = renderHook(() => useContainerList());
    expect(result.current.rows).toBeUndefined();
    await waitFor(() => expect(result.current.rows?.map((r) => r.name)).toEqual(['a', 'b']));
    expect(result.current.error).toBeUndefined();
    expect(listCalls()).toBe(1);
    unmount();
    act(() => pushChange({ project: '', seq: 1, resync: true }));
    await new Promise((r) => setTimeout(r, 20));
    expect(listCalls()).toBe(1);
  });

  it('patch applies a row at once and refresh fetches again', async () => {
    const { result } = renderHook(() => useContainerList());
    await waitFor(() => expect(result.current.rows).toHaveLength(2));
    act(() => result.current.patch(row('a', { state: 'exited' })));
    expect(result.current.rows?.[0].state).toBe('exited');
    act(() => result.current.refresh());
    await waitFor(() => expect(listCalls()).toBe(2));
  });

  it('StrictMode mounts one list', async () => {
    const { result } = renderHook(() => useContainerList(), { wrapper: StrictMode });
    await waitFor(() => expect(result.current.rows).toHaveLength(2));
    await new Promise((r) => setTimeout(r, 20));
    expect(listCalls()).toBe(1);
  });
});
