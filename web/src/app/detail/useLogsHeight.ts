import { useCallback, useRef, useState } from 'react';
import { clampLogsHeight, readLogsHeight, writeLogsHeight } from '$lib/stacks/console';

function defaultStorage(): Storage | undefined {
  try {
    return window.localStorage;
  } catch {
    return undefined;
  }
}

/** The console height: read once, clamped on every change, persisted only on `commit` (drag end, key step). */
export function useLogsHeight(storage: Storage | undefined = defaultStorage()) {
  const [height, setHeightState] = useState(() => readLogsHeight(storage));
  // commit() runs right after setHeight() in the same event, before a re-render, so it reads the ref.
  const latest = useRef(height);
  const setHeight = useCallback((next: number) => {
    const clamped = clampLogsHeight(Math.round(next));
    latest.current = clamped;
    setHeightState(clamped);
  }, []);
  const commit = useCallback(() => writeLogsHeight(storage, latest.current), [storage]);
  return { height, setHeight, commit };
}
