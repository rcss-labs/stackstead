import { useEffect, useRef } from 'react';
import type { RefObject } from 'react';
import { useLocation } from 'react-router';

/** How long to wait for a lazy screen's heading before falling back to focusing main. */
const HEADING_WAIT_MS = 2000;

/**
 * A client-side navigation does not move focus by itself, so keyboard and screen-reader users would stay
 * on the clicked link. After a path change (not a query change, not the first render) move focus to the new
 * page's h1, or to main, unless the page already put focus inside main. Waits for an h1 to appear so a lazy
 * screen still behind Suspense gets its heading focused once it renders.
 */
export function useFocusOnNavigate(mainRef: RefObject<HTMLElement | null>): void {
  const { pathname } = useLocation();
  const lastPath = useRef<string | undefined>(undefined);

  useEffect(() => {
    const previous = lastPath.current;
    lastPath.current = pathname;
    const main = mainRef.current;
    if (!main || previous === undefined || previous === pathname) return;

    let settled = false;
    let observer: MutationObserver | undefined;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const stopWaiting = () => {
      settled = true;
      observer?.disconnect();
      clearTimeout(timer);
    };
    const focusPage = () => {
      if (settled) return;
      stopWaiting();
      // The page's own effects ran before this one, so focus it placed inside main is kept.
      if (!main.isConnected || main.contains(document.activeElement)) return;
      const target = main.querySelector<HTMLElement>('h1') ?? main;
      target.setAttribute('tabindex', '-1');
      target.focus({ preventScroll: true });
    };

    if (main.querySelector('h1')) {
      focusPage();
    } else {
      observer = new MutationObserver(() => {
        if (main.querySelector('h1')) focusPage();
      });
      observer.observe(main, { childList: true, subtree: true });
      timer = setTimeout(focusPage, HEADING_WAIT_MS);
    }
    return stopWaiting;
  }, [pathname, mainRef]);
}
