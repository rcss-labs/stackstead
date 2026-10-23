import { useEffect, useRef, useState } from 'react';
import type { MouseEvent, RefObject } from 'react';
import { useLocation } from 'react-router';
import type { NavigateFunction } from 'react-router';
import { logout } from '$lib/api/session';
import { setUnsavedChanges, unsavedChanges } from '$lib/stores/guard';
import { toasts } from '$lib/stores/toasts';
import { useStore } from './useStore';

export interface UnsavedGuardOptions {
  navigate: NavigateFunction;
  /** Shared with the session guard: set while logging out so Login carries no `next`. */
  loggingOut: RefObject<boolean>;
}

export interface UnsavedGuard {
  /** Put on the layout: a link click that would leave a page with unsaved editor changes asks first. */
  onClickCapture: (event: MouseEvent) => void;
  /** Navigation from outside the guarded frame (palette, toasts): asks first when there are unsaved changes. */
  go: (href: string) => void;
  pendingHref: string | undefined;
  pendingLogout: boolean;
  requestLogout: () => void;
  /** Confirmed: drop the changes and carry on with the held navigation or logout. */
  leave: () => void;
  cancel: () => void;
}

/** React Router stamps each history entry with an increasing `idx`; it tells how far a popstate moved. */
function historyIndex(): number | undefined {
  const idx = (window.history.state as { idx?: unknown } | null)?.idx;
  return typeof idx === 'number' ? idx : undefined;
}

/**
 * The link guard (React Router's blocker needs a data router, and the app uses
 * BrowserRouter, so this is a click capture, the logout path, and a popstate
 * hold that undoes Back/Forward until the user confirms) plus a beforeunload
 * prompt for reloads and tab closes while the shared dirty flag is set.
 */
export function useUnsavedGuard({ navigate, loggingOut }: UnsavedGuardOptions): UnsavedGuard {
  const dirty = useStore(unsavedChanges);
  const [pendingHref, setPendingHref] = useState<string | undefined>();
  const [pendingLogout, setPendingLogout] = useState(false);
  const location = useLocation();
  const currentIndex = useRef<number | undefined>(historyIndex());
  /** How far a held Back/Forward moved (history.go(delta) re-applies it), and a flag for our own undo. */
  const heldDelta = useRef(0);
  const undoing = useRef(false);
  const dirtyRef = useRef(dirty);
  dirtyRef.current = dirty;

  useEffect(() => {
    currentIndex.current = historyIndex();
  }, [location]);

  useEffect(() => {
    // Capture phase on window runs before the router's own popstate listener, so the held location never renders.
    function onPopState(ev: PopStateEvent) {
      if (undoing.current) {
        undoing.current = false;
        ev.stopImmediatePropagation();
        return;
      }
      const next = historyIndex();
      const from = currentIndex.current;
      if (!dirtyRef.current || next === undefined || from === undefined || next === from) return;
      ev.stopImmediatePropagation();
      heldDelta.current = next - from;
      undoing.current = true;
      setPendingHref(window.location.pathname + window.location.search + window.location.hash);
      window.history.go(from - next);
    }
    window.addEventListener('popstate', onPopState, true);
    return () => window.removeEventListener('popstate', onPopState, true);
  }, []);

  useEffect(() => {
    if (!dirty) return;
    const warn = (ev: BeforeUnloadEvent) => ev.preventDefault();
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [dirty]);

  async function handleLogout() {
    loggingOut.current = true;
    setUnsavedChanges(false);
    const { ok } = await logout();
    if (!ok) toasts.push({ kind: 'warning', message: 'Logout may not have completed on the server' });
    navigate('/login', { replace: true });
  }

  function onClickCapture(ev: MouseEvent) {
    if (!dirty || ev.defaultPrevented || ev.button !== 0) return;
    if (ev.ctrlKey || ev.metaKey || ev.shiftKey || ev.altKey) return;
    const a = (ev.target as Element | null)?.closest?.('a[href]') as HTMLAnchorElement | null;
    if (!a || (a.target && a.target !== '_self') || a.hasAttribute('download')) return;
    const url = new URL(a.href, window.location.href);
    if (url.origin !== window.location.origin) return;
    if (url.pathname === window.location.pathname && url.search === window.location.search) return;
    ev.preventDefault();
    ev.stopPropagation();
    setPendingHref(url.pathname + url.search + url.hash);
  }

  function go(href: string) {
    const url = new URL(href, window.location.href);
    const samePage = url.pathname === window.location.pathname && url.search === window.location.search;
    if (dirty && !samePage) setPendingHref(url.pathname + url.search + url.hash);
    else navigate(href);
  }

  function requestLogout() {
    if (dirty) setPendingLogout(true);
    else void handleLogout();
  }

  function leave() {
    const href = pendingHref;
    const logoutToo = pendingLogout;
    setPendingHref(undefined);
    setPendingLogout(false);
    setUnsavedChanges(false);
    const delta = heldDelta.current;
    heldDelta.current = 0;
    if (logoutToo) void handleLogout();
    else if (delta !== 0) window.history.go(delta);
    else if (href) navigate(href);
  }

  function cancel() {
    heldDelta.current = 0;
    setPendingHref(undefined);
    setPendingLogout(false);
  }

  return { onClickCapture, go, pendingHref, pendingLogout, requestLogout, leave, cancel };
}
