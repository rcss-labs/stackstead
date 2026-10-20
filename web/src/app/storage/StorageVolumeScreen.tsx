import { useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { api, ApiError } from '$lib/api/client';
import type { StoragePlanRequest, StorageVolumeDetail } from '$lib/api/types';
import { live } from '$lib/stores/live';
import { deleteDisabledReason, sizeText } from '$lib/storage/storage';
import { Button } from '../components/Button';
import { RelativeTime } from '../components/RelativeTime';
import { Spinner } from '../components/Spinner';
import { Tag } from '../components/Tag';
import { useStore } from '../hooks/useStore';
import { CleanupDialog } from './CleanupDialog';
import { ConsumersCard, DeclarationsCard, LabelsDetails } from './DetailSections';
import styles from './DetailScreen.module.css';
import { useStorageRefresh } from './useStorageRefresh';

// A 404 means the volume is gone (null); any other failure surfaces as the screen error.
async function loadVolume(name: string): Promise<StorageVolumeDetail | null> {
  try {
    return await api.storageVolume(name);
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return null;
    throw e;
  }
}

export function StorageVolumeScreen({ name }: { name: string }) {
  const navigate = useNavigate();
  const { data: item, error } = useStorageRefresh(() => loadVolume(name), () => false);
  const liveState = useStore(live);
  const dockerDown = liveState.docker === 'unavailable' || liveState.events === 'disconnected';
  const reason = item ? deleteDisabledReason(item, dockerDown) : undefined;
  const [cleanup, setCleanup] = useState<StoragePlanRequest | undefined>();

  const title = item ? item.name : error ? 'Volume unavailable' : item === null ? 'Volume not found' : 'Volume details';

  return (
    <section className={styles.page}>
      <Link className={styles.back} to="/storage?tab=volumes">
        Back to volumes
      </Link>
      <header className={styles.head}>
        <h1 className={styles.title}>{title}</h1>
        {item && (
          <>
            <Tag>{item.containers.length ? 'in use' : item.protection ? 'unused · protected' : 'unused'}</Tag>
            <Button variant="danger" className={styles.deleteButton} disabledReason={reason} onClick={() => setCleanup({ kind: 'volume', name: item.name })}>
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
          {item.protection && (
            <p className={styles.muted}>
              {reason} ·{' '}
              <Link className={styles.link} to={`/stacks/${encodeURIComponent(item.protection.stack)}`}>
                {item.protection.stack}
              </Link>
            </p>
          )}
          <dl className={`${styles.card} ${styles.dl}`}>
            <dt>Driver</dt>
            <dd>{item.driver}</dd>
            <dt>Scope</dt>
            <dd>{item.scope}</dd>
            <dt>Created</dt>
            <dd>
              <RelativeTime iso={item.createdAt} />
            </dd>
            <dt>Size</dt>
            <dd>{sizeText(item.size, item.sizesPending)}</dd>
            <dt>Mountpoint</dt>
            <dd>{item.mountpoint || '—'}</dd>
            <dt>Project</dt>
            <dd>{item.project || '—'}</dd>
          </dl>
          <ConsumersCard consumers={item.containers} />
          <DeclarationsCard declarations={item.declaredBy} />
          <LabelsDetails labels={item.labels} />
        </>
      ) : item === undefined ? (
        <p className={styles.cluster}>
          <Spinner /> Loading volume…
        </p>
      ) : null}
      {/* Outside the data branches: a refresh that turns the page into 404 or an error must not unmount a running cleanup. */}
      {cleanup && <CleanupDialog request={cleanup} onClose={() => setCleanup(undefined)} onFinished={() => navigate('/storage?tab=volumes')} />}
    </section>
  );
}
