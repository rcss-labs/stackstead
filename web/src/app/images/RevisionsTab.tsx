import { useCallback, useEffect, useState } from 'react';
import type { ImageRevision, ImageRevisionDetail, LibraryImageDetail } from '$lib/api/imageTypes';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { EmptyState } from '../components/EmptyState';
import { RelativeTime } from '../components/RelativeTime';
import { useServer } from '../servers/ServerContext';
import { DiffView } from '../stacks/DiffView';
import styles from './images.module.css';

export function RevisionsTab({ image, onChanged }: { image: LibraryImageDetail; onChanged(): void }) {
  const { api } = useServer();
  const slug = image.slug;
  const [revisions, setRevisions] = useState<ImageRevision[]>();
  const [error, setError] = useState<string>();
  const [selected, setSelected] = useState<number>();
  const [detail, setDetail] = useState<ImageRevisionDetail>();
  const [restoring, setRestoring] = useState(false);

  const load = useCallback(async () => {
    try {
      setRevisions(await api.imageRevisions(slug));
      setError(undefined);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [api, slug]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (selected === undefined) return;
    let cancelled = false;
    setDetail(undefined);
    api
      .imageRevision(slug, selected)
      .then((d) => !cancelled && setDetail(d))
      .catch((e: unknown) => !cancelled && setError(e instanceof Error ? e.message : String(e)));
    return () => {
      cancelled = true;
    };
  }, [api, slug, selected]);

  async function restore(n: number) {
    setRestoring(true);
    try {
      const created = await api.restoreImageRevision(slug, n);
      toasts.push({ kind: 'success', message: `Restored revision ${n} as revision ${created.number}` });
      await load();
      onChanged();
    } catch (e) {
      toasts.push({ kind: 'danger', message: `Could not restore: ${e instanceof Error ? e.message : String(e)}` });
    } finally {
      setRestoring(false);
    }
  }

  return (
    <div className={styles.page}>
      {error && (
        <Banner kind="danger" title="Could not load revisions">
          {error}
        </Banner>
      )}
      {revisions && revisions.length === 0 && (
        <div className={styles.card}>
          <EmptyState title="No revisions yet" description="Every save or file change to the build context creates a revision." />
        </div>
      )}
      {revisions && revisions.length > 0 && (
        <div className={styles.card}>
          <ul className={styles.revisions} aria-label="Revisions">
            {revisions.map((r) => (
              <li key={r.number}>
                <button type="button" className={styles.revision} aria-current={r.number === selected} aria-label={`Revision ${r.number}: ${r.summary}`} onClick={() => setSelected(r.number)}>
                  <span className={styles.revNumber}>r{r.number}</span>
                  <span>{r.summary}</span>
                  <span className={styles.revTime}>
                    <RelativeTime iso={r.createdAt} />
                  </span>
                </button>
              </li>
            ))}
          </ul>
        </div>
      )}
      {selected !== undefined && (
        <div className={styles.card}>
          <div className={styles.pad}>
            <div className={styles.row}>
              <strong>Revision {selected}</strong>
              <span className={styles.spacer} />
              <Button variant="secondary" size="sm" loading={restoring} aria-label={`Restore revision ${selected}`} onClick={() => void restore(selected)}>
                Restore
              </Button>
            </div>
            {!detail && <p role="status">Loading revision…</p>}
            {detail && (
              <>
                {detail.dockerfileChanged ? (
                  <DiffView original={detail.previous} modified={detail.dockerfile} ariaLabel={`Dockerfile changes in revision ${detail.number}`} language="dockerfile" />
                ) : (
                  <p className={styles.muted}>The Dockerfile did not change in this revision.</p>
                )}
                <ul className={styles.mono}>
                  {detail.files.map((f) => (
                    <li key={f.path}>{f.path}</li>
                  ))}
                </ul>
              </>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
