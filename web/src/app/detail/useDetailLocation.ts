import { useCallback, useMemo } from 'react';
import { useNavigate, useSearchParams } from 'react-router';
import type { StackDetail } from '$lib/api/types';
import { itemHref, resolveDetailLocation, type DetailItem, type DetailLocation, type ServiceTab } from '$lib/stacks/detailLocation';

import { useServer } from '../servers/ServerContext';
export type DetailLocationState = DetailLocation & {
  go: (href: string) => void;
  hrefFor: (item: DetailItem, tab?: ServiceTab) => string;
};

/** What the URL selects on this stack's page, resolved against the loaded detail, plus navigation helpers. */
export function useDetailLocation(name: string, detail: StackDetail | undefined): DetailLocationState {
  const [query] = useSearchParams();
  const navigate = useNavigate();
  const { href } = useServer();
  const location = useMemo(() => resolveDetailLocation(query, detail), [query, detail]);
  const go = useCallback((href: string) => void navigate(href), [navigate]);
  const hrefFor = useCallback((item: DetailItem, tab?: ServiceTab) => href(itemHref(name, item, tab)), [name, href]);
  return { ...location, go, hrefFor };
}
