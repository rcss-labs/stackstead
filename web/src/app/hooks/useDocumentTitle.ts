import { useEffect } from 'react';
import type { AppRoute } from '../routes';

/** `<Page> · Stackstead` for routes with a static title, only while signed in; entity screens title themselves. */
export function useDocumentTitle(route: AppRoute | undefined, authenticated: boolean): void {
  const title = route?.title;
  useEffect(() => {
    if (authenticated && title) document.title = `${title} · Stackstead`;
  }, [title, authenticated]);
}
