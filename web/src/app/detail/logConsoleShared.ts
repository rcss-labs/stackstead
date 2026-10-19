import type { LogError } from '$lib/api/types';

/** Colour slot for a service: its position among the sorted names, wrapped over the four console series. */
export function seriesVar(index: number): string {
  return `var(--series-${(index % 4) + 1})`;
}

/** Identity of a per-container error, for dismissing it. */
export function errorKey(e: LogError): string {
  return `${e.container}\0${e.error}`;
}
