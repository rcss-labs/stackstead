import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import type { Library } from '$lib/api/imageTypes';
import { groupByOwner, imageHref, ownerIsViewed } from '$lib/images/library';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { EmptyState } from '../components/EmptyState';
import { Spinner } from '../components/Spinner';
import { Tag } from '../components/Tag';
import { useStore } from '../hooks/useStore';
import { useServer } from '../servers/ServerContext';
import styles from './images.module.css';
import { NewImageDialog } from './NewImageDialog';
import { VersionStatus } from './VersionStatus';

export function ImagesScreen() {
  const scope = useServer();
  const { api, href } = scope;
  const tick = useStore(scope.operationsRefresh);
  const navigate = useNavigate();
  const [library, setLibrary] = useState<Library>();
  const [error, setError] = useState<string>();
  const [creating, setCreating] = useState(false);
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    document.title = 'Images · Stackstead';
  }, []);

  useEffect(() => {
    let cancelled = false;
    api
      .images()
      .then((l) => {
        if (cancelled) return;
        setLibrary(l);
        setError(undefined);
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      });
    return () => {
      cancelled = true;
    };
  }, [api, tick, attempt]);

  const images = useMemo(() => groupByOwner(library?.images ?? []).flatMap((g) => g.images), [library]);

  return (
    <section className={styles.page} aria-labelledby="images-heading">
      <header className={styles.head}>
        <h1 id="images-heading" className={styles.title}>
          Images
        </h1>
        <span className={styles.spacer} />
        <Button variant="primary" onClick={() => setCreating(true)}>
          New image
        </Button>
      </header>

      {error && (
        <Banner kind="danger" title="Could not load images">
          {error}{' '}
          <Button variant="secondary" size="sm" onClick={() => setAttempt((n) => n + 1)}>
            Retry
          </Button>
        </Banner>
      )}
      {library?.servers
        .filter((server) => !server.local && server.status !== 'reachable')
        .map((server) => (
          <Banner key={server.id} kind="warning" title="Some images are missing">
            {server.status === 'old'
              ? `${server.name} runs an older Stackstead without the image library`
              : `${server.name} could not be reached`}
          </Banner>
        ))}
      {!library && !error && <Spinner />}
      {library && images.length === 0 && (
        <div className={styles.card}>
          <EmptyState title="No images yet" description="Upload a Dockerfile and its files, or point at a GitHub repository, then build versions to use in stacks." />
        </div>
      )}
      {library && images.length > 0 && (
        <div className={`${styles.card} ${styles.scroll}`}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>Name</th>
                <th>Source</th>
                <th>Owner</th>
                <th>Latest tag</th>
                <th>Last build</th>
                <th>Push targets</th>
              </tr>
            </thead>
            <tbody>
              {images.map((image) => (
                <tr key={`${image.owner.id}/${image.slug}`}>
                  <td>
                    <Link to={imageHref(image, scope)}>{image.name}</Link>
                  </td>
                  <td>{image.source}</td>
                  <td>{ownerIsViewed(image, scope.id) ? image.owner.name : <Tag tone="info">{image.owner.name}</Tag>}</td>
                  <td className={styles.mono}>{image.latestVersion?.tag ?? '—'}</td>
                  <td>{image.lastBuild ? <VersionStatus status={image.lastBuild.status} /> : '—'}</td>
                  <td>{image.pushTargets.length}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {creating && (
        <NewImageDialog
          onClose={() => setCreating(false)}
          onCreated={(image) => {
            setCreating(false);
            void navigate(href(`/images/${encodeURIComponent(image.slug)}`));
          }}
        />
      )}
    </section>
  );
}
