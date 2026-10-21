import { useCallback, useEffect, useRef, useState } from 'react';
import { clampLogsHeight, logsHeightBounds, LOGS_HEIGHT_MOBILE, readLogsHeight, writeLogsHeight } from '$lib/stacks/console';

function defaultStorage(): Storage | undefined {
  try {
    return window.localStorage;
  } catch {
    return undefined;
  }
}

function viewportHeight(): number | undefined {
  return typeof window === 'undefined' ? undefined : window.innerHeight;
}

/** Clamp the displayed height without replacing the user's preferred height when the window shrinks. */
export function useLogsHeight(storage: Storage | undefined = defaultStorage()) {
  const [preferredHeight, setPreferredHeight] = useState(() => readLogsHeight(storage));
  const [viewport, setViewport] = useState(viewportHeight);
  useEffect(() => {
    const onResize = () => setViewport(viewportHeight());
    window.addEventListener('resize', onResize);
    return () => window.removeEventListener('resize', onResize);
  }, []);
  const bounds = logsHeightBounds(viewport);
  const height = clampLogsHeight(preferredHeight, viewport);
  // commit() runs right after setHeight() in the same event, before a re-render, so it reads the ref.
  const latest = useRef(preferredHeight);
  const setHeight = useCallback((next: number) => {
    const clamped = clampLogsHeight(Math.round(next), viewport);
    latest.current = clamped;
    setPreferredHeight(clamped);
  }, [viewport]);
  const commit = useCallback(() => {
    if (latest.current !== undefined) writeLogsHeight(storage, latest.current);
  }, [storage]);
  return { height, min: bounds.min, max: bounds.max, mobileHeight: Math.min(LOGS_HEIGHT_MOBILE, bounds.max), setHeight, commit };
}
