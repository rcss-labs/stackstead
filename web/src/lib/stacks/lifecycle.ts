import type { StackStatus } from '$lib/api/types';
import type { LiveState } from '$lib/stores/live';

/** Shown while another operation holds the stack. */
export const BUSY_REASON = 'an operation is already running';

export interface LifecycleInput {
  live: Pick<LiveState, 'compose' | 'docker' | 'events'>;
  status?: StackStatus;
  parseError?: string;
  busy?: boolean;
}

/** Why lifecycle actions (up/down/restart/pull/build) are unavailable, or undefined when they are allowed. */
export function lifecycleDisabledReason(input: LifecycleInput): string | undefined {
  const { live, status, parseError, busy } = input;
  if (live.compose === 'unavailable') return 'docker compose plugin not found';
  if (live.docker === 'unavailable' || live.events === 'disconnected') return 'Docker is unreachable';
  if (parseError || status === 'unavailable') return 'the compose file cannot be parsed';
  if (busy) return BUSY_REASON;
  return undefined;
}
