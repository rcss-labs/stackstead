import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import type { Library } from '$lib/api/imageTypes';
import { formatBytes } from '$lib/format';
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

      <p className={styles.lead}>
        Images you build and version here. For everything on disk, see <Link to={href('/storage')}>Storage</Link>.
      </p>
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
        <ul className={styles.grid} aria-label="Images">
          {images.map((image) => (
            <li key={`${image.owner.id}/${image.slug}`} className={styles.imageCard}>
              <Link to={imageHref(image, scope)} className={styles.cardName}>
                {image.name}
              </Link>
              <div className={styles.cardMeta}>
                <span>{image.source}</span>
                {ownerIsViewed(image, scope.id) ? <span>{image.owner.name}</span> : <Tag tone="info">{image.owner.name}</Tag>}
              </div>
              <dl className={styles.facts}>
                <dt>Latest tag</dt>
                <dd className={styles.mono}>{image.latestVersion?.tag ?? '—'}</dd>
                <dt>Last build</dt>
                <dd>{image.lastBuild ? <VersionStatus status={image.lastBuild.status} /> : '—'}</dd>
                <dt>Push targets</dt>
                <dd>{image.pushTargets.length}</dd>
                {image.latestVersion && image.latestVersion.size > 0 && (
                  <>
                    <dt>Size</dt>
                    <dd>{formatBytes(image.latestVersion.size)}</dd>
                  </>
                )}
              </dl>
            </li>
          ))}
        </ul>
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
