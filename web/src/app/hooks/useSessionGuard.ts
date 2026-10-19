import { useCallback, useEffect, useRef } from 'react';
import type { RefObject } from 'react';
import { useLocation, useNavigate } from 'react-router';
import { expireSession, session } from '$lib/api/session';
import { safeNext } from '$lib/router';
import { get } from '$lib/store';
import { live, stopLive } from '$lib/stores/live';
import { useStore } from './useStore';

/**
 * Returns a stable handler for "the server says this session is gone": stops live updates, expires
 * the session and sends the user to Login carrying `next` (except from `/` and `/login`).
 * It reads the session and URL at call time, because 401s arrive outside React renders and two can
 * land in the same tick: once the session is no longer authenticated a late 401 must not clobber `next`.
 */
export function useSessionLost(): () => void {
  const navigate = useNavigate();
  const navigateRef = useRef(navigate);
  navigateRef.current = navigate;
  return useCallback(() => {
    if (get(session).state !== 'authenticated') return;
    stopLive();
    const here = window.location.pathname + window.location.search;
    expireSession();
    navigateRef.current(here.startsWith('/login') || here === '/' ? '/login' : `/login?next=${encodeURIComponent(here)}`, { replace: true });
  }, []);
}

export interface SessionGuardOptions {
  /** False until this page load has asked the server; nothing is decided before that. */
  booted: boolean;
  /** Set while a user-initiated logout runs, so Login does not carry `next`. */
  loggingOut: RefObject<boolean>;
}

/** The App.svelte session guards: redirects for each session state, and a stream 401 ends the session. */
export function useSessionGuard({ booted, loggingOut }: SessionGuardOptions): void {
  const navigate = useNavigate();
  const { pathname, search } = useLocation();
  const { state } = useStore(session);
  const { connection } = useStore(live);
  const sessionLost = useSessionLost();

  useEffect(() => {
    if (!booted) return;
    // The URL can be ahead of the router's committed location (navigations are transitions), so
    // decide on the address bar: a redirect already under way must not be overridden.
    const path = window.location.pathname;
    if (state === 'needs-setup' && path !== '/setup') {
      navigate('/setup', { replace: true });
    } else if (state === 'anonymous' && path !== '/login') {
      if (loggingOut.current) navigate('/login', { replace: true });
      else navigate(`/login?next=${encodeURIComponent(path + window.location.search)}`, { replace: true });
    }
    if (state === 'authenticated') {
      loggingOut.current = false;
      if (path === '/login') navigate(safeNext(new URLSearchParams(window.location.search).get('next')), { replace: true });
      else if (path === '/setup') navigate('/', { replace: true });
    }
  }, [booted, state, pathname, search, navigate, loggingOut]);

  // The events stream answering 401 means the session is gone even if no API call has noticed yet.
  useEffect(() => {
    if (connection === 'unauthorized' && state === 'authenticated') sessionLost();
  }, [connection, state, sessionLost]);
}
