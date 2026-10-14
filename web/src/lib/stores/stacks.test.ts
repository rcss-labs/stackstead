import { get, writable } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Change, StackSummary } from '../api/types';
import { refreshStacks, stacks, startStacks } from './stacks';

type Ch = (Change & { resync?: true }) | null;

const stack = (name: string, over: Partial<StackSummary> = {}): StackSummary => ({
  name,
  path: `/srv/${name}`,
  project: name,
  origin: 'managed',
  status: 'running',
  serviceCount: 1,
  running: 1,
  ...over,
});

let changes: ReturnType<typeof writable<Ch>>;
let list: ReturnType<typeof vi.fn<() => Promise<StackSummary[]>>>;
let stop: (() => void) | undefined;
const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);
let seq = 0;
const emit = (project: string, resync?: true) => changes.set({ project, seq: ++seq, ...(resync ? { resync } : {}) });

beforeEach(() => {
  vi.useFakeTimers();
  changes = writable<Ch>(null);
  list = vi.fn(async () => [stack('web')]);
});
afterEach(() => {
  stop?.();
  stop = undefined;
  vi.useRealTimers();
});

describe('stacks store', () => {
  it('loads once on start and exposes items', async () => {
    stop = startStacks({ list, changes });
    expect(get(stacks).loading).toBe(true);
    await tick();
    expect(list).toHaveBeenCalledTimes(1);
    expect(get(stacks)).toMatchObject({ loading: false, items: [{ name: 'web' }] });
  });

  it('TestResyncRefetchesList', async () => {
    stop = startStacks({ list, changes });
    await tick();
    emit('', true);
    await tick();
    expect(list).toHaveBeenCalledTimes(2);
  });

  it('refetches for a change on an unknown project (new stack appeared)', async () => {
    stop = startStacks({ list, changes });
    await tick();
    emit('brand-new');
    await tick(300);
    expect(list).toHaveBeenCalledTimes(2);
  });

  it('debounces a burst of changes into one refetch', async () => {
    stop = startStacks({ list, changes });
    await tick();
    emit('web');
    await tick(50);
    emit('web');
    await tick(50);
    emit('web');
    await tick(299);
    expect(list).toHaveBeenCalledTimes(1);
    await tick(1);
    expect(list).toHaveBeenCalledTimes(2);
  });

  it('ignores a stale response that resolves after a newer one', async () => {
    const resolvers: Array<(v: StackSummary[]) => void> = [];
    list.mockImplementation(() => new Promise((r) => resolvers.push(r)));
    stop = startStacks({ list, changes });
    await tick();
    emit('', true);
    await tick();
    expect(resolvers).toHaveLength(2);
    resolvers[1]([stack('new')]);
    await tick();
    resolvers[0]([stack('old')]);
    await tick();
    expect(get(stacks).items.map((s) => s.name)).toEqual(['new']);
  });

  it('keeps items and reports the error when a refetch fails', async () => {
    stop = startStacks({ list, changes });
    await tick();
    list.mockRejectedValueOnce(new Error('boom'));
    await refreshStacks();
    expect(get(stacks)).toMatchObject({ error: 'boom', items: [{ name: 'web' }] });
    await refreshStacks();
    expect(get(stacks).error).toBeUndefined();
  });

  it('stops reacting and drops pending refetches after the returned stop function', async () => {
    stop = startStacks({ list, changes });
    await tick();
    emit('web');
    stop();
    await tick(500);
    emit('web');
    await tick(500);
    expect(list).toHaveBeenCalledTimes(1);
  });
});
