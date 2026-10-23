import { useCallback, useEffect, useState } from 'react';
import { ApiError } from '$lib/api/client';
import type { LibraryImageDetail } from '$lib/api/imageTypes';
import { useStore } from '../hooks/useStore';
import { useServer } from '../servers/ServerContext';

/** One image's detail; it reloads when an operation may have ended and on `reload()`. */
export function useImageDetail(slug: string) {
  const { api, operationsRefresh } = useServer();
  const tick = useStore(operationsRefresh);
  const [detail, setDetail] = useState<LibraryImageDetail>();
  const [error, setError] = useState<ApiError | Error>();
  const [version, setVersion] = useState(0);

  useEffect(() => {
    let cancelled = false;
    api
      .image(slug)
      .then((d) => {
        if (cancelled) return;
        setDetail(d);
        setError(undefined);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e : new Error(String(e)));
      });
    return () => {
      cancelled = true;
    };
  }, [api, slug, tick, version]);

  const reload = useCallback(() => setVersion((v) => v + 1), []);
  return { detail, error, notFound: error instanceof ApiError && error.status === 404, reload };
}
