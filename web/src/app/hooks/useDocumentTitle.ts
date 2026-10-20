import { useEffect } from 'react';
import type { AppRoute } from '../routes';

/** `<Page> · Stackstead` for routes with a static title, only while signed in; entity screens title themselves. */
export function useDocumentTitle(route: AppRoute | undefined, authenticated: boolean, serverName?: string): void {
  const title = route?.title;
  useEffect(() => {
    if (authenticated && title) document.title = serverName ? `${title} · ${serverName} · Stackstead` : `${title} · Stackstead`;
  }, [title, authenticated, serverName]);
}
