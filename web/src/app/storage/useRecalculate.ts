import { useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError } from '$lib/api/client';

const DEFAULT_COOLDOWN_MS = 60_000;

export interface Recalculate {
  /** Why Recalculate is unavailable right now; undefined when it can run. */
  reason: string | undefined;
  recalculating: boolean;
  error: string;
  recalculate: () => Promise<void>;
}

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** Requests a fresh size reading, then holds the button for the server's cooldown (60 s, or its 429 retryAfter). */
export function useRecalculate(dockerDown: boolean, refresh: () => void): Recalculate {
  const [recalculating, setRecalculating] = useState(false);
  const [secondsLeft, setSecondsLeft] = useState(0);
  const [error, setError] = useState('');
  const cooldownUntil = useRef(0);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const mounted = useRef(false);
  // Always the latest props/state for the async continuation and timer, without re-creating them.
  const refreshRef = useRef(refresh);
  refreshRef.current = refresh;

  const updateCooldown = useCallback(() => {
    clearTimeout(timer.current);
    const left = Math.max(0, Math.ceil((cooldownUntil.current - Date.now()) / 1000));
    setSecondsLeft(left);
    // The tick chain runs only while mounted and visible; a hidden tab re-ticks on visibilitychange.
    if (mounted.current && left > 0 && document.visibilityState !== 'hidden') timer.current = setTimeout(updateCooldown, 1000);
  }, []);

  useEffect(() => {
    mounted.current = true;
    document.addEventListener('visibilitychange', updateCooldown);
    return () => {
      mounted.current = false;
      document.removeEventListener('visibilitychange', updateCooldown);
      clearTimeout(timer.current);
    };
  }, [updateCooldown]);

  const reason = dockerDown
    ? 'Docker is unavailable'
    : recalculating
      ? 'Requesting a size reading'
      : secondsLeft
        ? `Recalculate available in ${secondsLeft}s`
        : undefined;

  const recalculate = useCallback(async () => {
    if (reason) return;
    setRecalculating(true);
    setError('');
    try {
      await api.refreshStorage();
      cooldownUntil.current = Date.now() + DEFAULT_COOLDOWN_MS;
      refreshRef.current();
    } catch (e) {
      setError(message(e));
      if (e instanceof ApiError && e.status === 429) {
        const retry = (e.body as { retryAfter?: unknown } | undefined)?.retryAfter;
        cooldownUntil.current = Date.now() + (typeof retry === 'number' && retry > 0 ? retry : 60) * 1000;
      }
    } finally {
      setRecalculating(false);
      updateCooldown();
    }
  }, [reason, updateCooldown]);

  return { reason, recalculating, error, recalculate };
}
