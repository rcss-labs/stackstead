import { get, writable } from 'svelte/store';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { LiveChange } from './live';
import { createRefreshList } from './refreshList';

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

it('patches by the supplied key and drops an older fetch without adding missing rows', async () => {
  type Row = { slug: string; value: number };
  let resolve!: (rows: Row[]) => void;
  const list = vi.fn<() => Promise<Row[]>>().mockResolvedValue([{ slug: 'a', value: 1 }, { slug: 'b', value: 2 }]);
  const c = createRefreshList({ list, changes: writable<LiveChange | null>(null), key: (r) => r.slug });
  c.start();
  await vi.advanceTimersByTimeAsync(0);
  list.mockImplementationOnce(() => new Promise((r) => (resolve = r)));
  c.refresh();
  c.patch({ slug: 'b', value: 3 });
  c.patch({ slug: 'missing', value: 4 });
  resolve([{ slug: 'a', value: 0 }]);
  await vi.advanceTimersByTimeAsync(0);
  expect(get(c.rows)).toEqual([{ slug: 'a', value: 1 }, { slug: 'b', value: 3 }]);
  c.stop();
});

it('refreshes after one second of sustained changes with generic rows', async () => {
  const changes = writable<LiveChange | null>(null);
  const list = vi.fn(async () => [{ slug: 'a' }]);
  const c = createRefreshList({ list, changes, key: (r) => r.slug });
  c.start();
  await vi.advanceTimersByTimeAsync(0);
  for (let seq = 0; seq < 5; seq++) {
    changes.set({ project: '', seq });
    await vi.advanceTimersByTimeAsync(200);
  }
  expect(list).toHaveBeenCalledTimes(2);
  expect(get(c.rows)).toEqual([{ slug: 'a' }]);
  c.stop();
});
