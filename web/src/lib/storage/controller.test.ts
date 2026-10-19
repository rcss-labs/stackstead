import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { get, writable } from '$lib/store';
import { createStorageRefresh } from './controller';
import type { LiveChange } from '../stores/live';
let visibility = 'visible';
beforeEach(() => { vi.useFakeTimers(); visibility = 'visible'; vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility as DocumentVisibilityState); });
afterEach(() => { vi.useRealTimers(); vi.restoreAllMocks(); });
const setup = () => {
  const changes = writable<LiveChange | null>(null); const operations = writable(0);
  const load = vi.fn().mockResolvedValue({ refreshing: false, value: 1 });
  const controller = createStorageRefresh<{ refreshing: boolean; value: number }>({ load, changes, operations, refreshing: (d: { refreshing: boolean }) => d.refreshing });
  return { changes, operations, load, controller };
};
it('loads on mount/change/resync/end, polling only pending visible readings and cleans up', async () => {
  const { controller, load, changes, operations } = setup(); controller.start(); await vi.advanceTimersByTimeAsync(0);
  expect(load).toHaveBeenCalledTimes(1); await vi.advanceTimersByTimeAsync(20000); expect(load).toHaveBeenCalledTimes(1);
  load.mockResolvedValue({ refreshing: true, value: 2 }); changes.set({ project: '', seq: 1 }); changes.set({ project: '', seq: 2 });
  await vi.advanceTimersByTimeAsync(299); expect(load).toHaveBeenCalledTimes(1); await vi.advanceTimersByTimeAsync(1); expect(load).toHaveBeenCalledTimes(2);
  await vi.advanceTimersByTimeAsync(10000); expect(load).toHaveBeenCalledTimes(3);
  visibility = 'hidden'; document.dispatchEvent(new Event('visibilitychange')); changes.set({ project: '', seq: 3 }); operations.set(1);
  await vi.advanceTimersByTimeAsync(20000); expect(load).toHaveBeenCalledTimes(3);
  visibility = 'visible'; document.dispatchEvent(new Event('visibilitychange')); await vi.advanceTimersByTimeAsync(0); expect(load).toHaveBeenCalledTimes(4);
  changes.set({ project: '', seq: 4, resync: true }); await vi.advanceTimersByTimeAsync(0); expect(load).toHaveBeenCalledTimes(5);
  operations.set(2); await vi.advanceTimersByTimeAsync(300); expect(load).toHaveBeenCalledTimes(6);
  controller.stop(); changes.set({ project: '', seq: 5 }); await vi.advanceTimersByTimeAsync(20000); expect(load).toHaveBeenCalledTimes(6); expect(vi.getTimerCount()).toBe(0);
});
it('ignores outdated and hidden responses and surfaces current failures', async () => {
  const { controller, load } = setup(); let resolve!: (value: { refreshing: boolean; value: number }) => void;
  load.mockImplementationOnce(() => new Promise(r => resolve = r)); controller.start(); controller.refresh(); await vi.advanceTimersByTimeAsync(0);
  resolve({ refreshing: false, value: 9 }); await vi.advanceTimersByTimeAsync(0); expect(get(controller.data)?.value).toBe(1);
  load.mockImplementationOnce(() => new Promise(r => resolve = r)); controller.refresh(); visibility = 'hidden'; document.dispatchEvent(new Event('visibilitychange'));
  resolve({ refreshing: false, value: 10 }); await vi.advanceTimersByTimeAsync(0); expect(get(controller.data)?.value).toBe(1);
  load.mockRejectedValueOnce(new Error('Docker offline')); visibility = 'visible'; document.dispatchEvent(new Event('visibilitychange')); await vi.advanceTimersByTimeAsync(0); expect(get(controller.error)).toBe('Docker offline'); controller.stop();
});
it('does not fetch when mounted hidden', async () => { visibility = 'hidden'; const { controller, load } = setup(); controller.start(); controller.refresh(); await vi.advanceTimersByTimeAsync(30000); expect(load).not.toHaveBeenCalled(); controller.stop(); });
