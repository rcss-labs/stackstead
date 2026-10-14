// Trailing debounce with a ceiling: bursts collapse into one call, but sustained
// activity still runs at least every `maxWaitMs`.
export const DEBOUNCE_MS = 300;
export const MAX_WAIT_MS = 1000;

export interface Debounced {
  /** Schedule a run; repeated calls extend the wait up to the max wait. */
  trigger(): void;
  /** Drop anything scheduled. */
  cancel(): void;
  /** Run now, dropping anything scheduled. */
  flush(): void;
}

export function debounce(run: () => void, delayMs = DEBOUNCE_MS, maxWaitMs = MAX_WAIT_MS): Debounced {
  let timer: ReturnType<typeof setTimeout> | undefined;
  let pendingSince: number | undefined;
  const cancel = () => {
    clearTimeout(timer);
    timer = undefined;
    pendingSince = undefined;
  };
  return {
    trigger() {
      const now = Date.now();
      pendingSince ??= now;
      clearTimeout(timer);
      timer = setTimeout(
        () => {
          cancel();
          run();
        },
        Math.max(0, Math.min(delayMs, pendingSince + maxWaitMs - now)),
      );
    },
    cancel,
    flush() {
      cancel();
      run();
    },
  };
}
