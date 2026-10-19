import type { LogError } from '$lib/api/types';

/** CSS colour of a series slot (0-based, as lib/stacks/metrics serviceColorIndex returns), matching the metrics charts. */
export function seriesVar(index: number): string {
  return `var(--series-${index + 1})`;
}

/** Identity of a per-container error, for dismissing it. */
export function errorKey(e: LogError): string {
  return `${e.container}\0${e.error}`;
}
