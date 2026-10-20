import { useEffect, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { ApiError, type Api } from '$lib/api/client';
import type { StorageImageDetail, StoragePlanRequest } from '$lib/api/types';
import { deleteDisabledReason, sizeText } from '$lib/storage/storage';
import { Button } from '../components/Button';
import { CopyButton } from '../components/CopyButton';
import { RelativeTime } from '../components/RelativeTime';
import { Spinner } from '../components/Spinner';
import { Tag } from '../components/Tag';
import { useStore } from '../hooks/useStore';
import { useServer } from '../servers/ServerContext';
import { CleanupDialog } from './CleanupDialog';
import { ConsumersCard, DeclarationsCard, LabelsDetails } from './DetailSections';
import styles from './DetailScreen.module.css';
import { useStorageRefresh } from './useStorageRefresh';

// A 404 means the image is gone (null); any other failure surfaces as the screen error.
async function loadImage(api: Api, id: string): Promise<StorageImageDetail | null> {
  try {
    return await api.storageImage(id);
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

export function StorageImageScreen({ id }: { id: string }) {
  const navigate = useNavigate();
  const { api, live, href } = useServer();
  const { data: item, error } = useStorageRefresh(() => loadImage(api, id), () => false);
  const liveState = useStore(live);
  const dockerDown = liveState.docker === 'unavailable' || liveState.events === 'disconnected';
  const reason = item ? deleteDisabledReason(item, dockerDown) : undefined;
  const headingRef = useRef<HTMLHeadingElement>(null);
  const [cleanup, setCleanup] = useState<StoragePlanRequest | undefined>();

  // A refresh that turns the page into not-found removes the focused control; move focus to the heading instead of body.
  const gone = item === null;
  // A cleanup dialog stays mounted across the 404 and keeps focus until it closes; then the heading takes over.
  const cleanupOpen = cleanup !== undefined;
  useEffect(() => {
    if (gone && !cleanupOpen) headingRef.current?.focus({ preventScroll: true });
  }, [gone, cleanupOpen]);

  const title = item
    ? (item.tags[0] ?? item.id.replace(/^sha256:/, '').slice(0, 12))
    : error
      ? 'Image unavailable'
      : item === null
        ? 'Image not found'
        : 'Image details';

  return (
    <section className={styles.page}>
      <Link className={styles.back} to={href('/storage?tab=images')}>
        Back to images
      </Link>
      <header className={styles.head}>
        <h1 ref={headingRef} tabIndex={-1} className={styles.title}>{title}</h1>
        {item && (
          <>
            <Tag>{item.containers.length ? 'in use' : item.dangling ? 'dangling' : 'no containers'}</Tag>
            <CopyButton text={item.id} label="Copy image ID" />
            <Button variant="danger" className={styles.deleteButton} disabledReason={reason} onClick={() => setCleanup({ kind: 'image', ref: item.id })}>
              Delete
            </Button>
          </>
        )}
      </header>
      {error ? (
        <p role="alert" className={styles.error}>
          {error}
        </p>
      ) : item ? (
        <>
          <dl className={`${styles.card} ${styles.dl}`}>
            <dt>Tags</dt>
            <dd>{item.tags.join(', ') || '—'}</dd>
            <dt>Digests</dt>
            <dd>{item.digests.join(', ') || '—'}</dd>
            <dt>Created</dt>
            <dd>
              <RelativeTime iso={item.created} />
            </dd>
            <dt>Size</dt>
            <dd>{sizeText(item.size)}</dd>
            <dt>Architecture / OS</dt>
            <dd>
              {item.architecture} / {item.os}
            </dd>
            <dt>Layers</dt>
            <dd>{item.layers}</dd>
          </dl>
          <ConsumersCard consumers={item.containers} />
          <DeclarationsCard declarations={item.declaredBy} />
          <LabelsDetails labels={item.labels} />
        </>
      ) : item === undefined ? (
        <p className={styles.cluster}>
          <Spinner /> Loading image…
        </p>
      ) : null}
      {/* Outside the data branches: a refresh that turns the page into 404 or an error must not unmount a running cleanup. */}
      {cleanup && <CleanupDialog request={cleanup} onClose={() => setCleanup(undefined)} onFinished={() => navigate(href('/storage?tab=images'))} />}
    </section>
  );
}
