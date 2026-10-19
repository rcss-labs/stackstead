import type { Operation } from '$lib/api/types';
import { runningOps } from '$lib/stores/runningOps';
import { useStore } from './useStore';

/** Running operations for the top bar and the palette; the store is started with the session in App. */
export function useRunningOps(): Operation[] {
  return useStore(runningOps);
}
