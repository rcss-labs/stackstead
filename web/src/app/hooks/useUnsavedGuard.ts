import { useState } from 'react';
import type { MouseEvent, RefObject } from 'react';
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
  pendingHref: string | undefined;
  pendingLogout: boolean;
  requestLogout: () => void;
  /** Confirmed: drop the changes and carry on with the held navigation or logout. */
  leave: () => void;
  cancel: () => void;
}

/**
 * The App.svelte link guard (divergence 1: React Router's blocker needs a data router, so this is a
 * click capture plus the logout path; back/forward are not guarded, as today).
 */
export function useUnsavedGuard({ navigate, loggingOut }: UnsavedGuardOptions): UnsavedGuard {
  const dirty = useStore(unsavedChanges);
  const [pendingHref, setPendingHref] = useState<string | undefined>();
  const [pendingLogout, setPendingLogout] = useState(false);

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
    if (logoutToo) void handleLogout();
    else if (href) navigate(href);
  }

  function cancel() {
    setPendingHref(undefined);
    setPendingLogout(false);
  }

  return { onClickCapture, pendingHref, pendingLogout, requestLogout, leave, cancel };
}
