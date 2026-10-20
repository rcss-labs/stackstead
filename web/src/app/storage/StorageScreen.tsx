import { useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import { api } from '$lib/api/client';
import type { ActionEntry, StorageBuildCache, StorageImages, StorageOverview, StoragePlanRequest, StorageVolumes } from '$lib/api/types';
import { filterImages, filterVolumes, imageChips, imageCounts, imageReclaimRange, pageView, sizeText, volumeChips, volumeCounts, type ImageChip, type VolumeChip } from '$lib/storage/storage';
import { kindFromQuery, reclaimableText, sortBySize, usageSummary, usedText, type StorageKind } from '$lib/storage/usage';
import { live } from '$lib/stores/live';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { RelativeTime } from '../components/RelativeTime';
import { Spinner } from '../components/Spinner';
import { useStore } from '../hooks/useStore';
import { Pager } from '../components/Pager';
import { CacheRows } from './CacheRows';
import { CleanupDialog } from './CleanupDialog';
import { ImageRows } from './ImageRows';
import { KindToolbar } from './KindToolbar';
import { RecentActions } from './RecentActions';
import styles from './StorageScreen.module.css';
import { StorageCell, StorageRow, StorageTable } from './StorageTable';
import { UsageCard } from './UsageCard';
import { VolumeRows } from './VolumeRows';
import { useRecalculate } from './useRecalculate';
import { useStorageRefresh } from './useStorageRefresh';

export interface StorageData {
  overview?: StorageOverview;
  images?: StorageImages;
  volumes?: StorageVolumes;
  cache?: StorageBuildCache;
  actions: ActionEntry[];
  errors: Record<'overview' | 'images' | 'volumes' | 'build-cache' | 'actions', string>;
}

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** Independent reads, so the persistent action log stays usable when Docker is down. */
export async function loadStorage(): Promise<StorageData> {
  const [overview, images, volumes, cache, actions] = await Promise.allSettled([
    api.storageOverview(),
    api.storageImages(),
    api.storageVolumes(),
    api.storageBuildCache(),
    api.recentActions({ kind: 'storage.', limit: 20 }),
  ]);
  return {
    overview: overview.status === 'fulfilled' ? overview.value : undefined,
    images: images.status === 'fulfilled' ? images.value : undefined,
    volumes: volumes.status === 'fulfilled' ? volumes.value : undefined,
    cache: cache.status === 'fulfilled' ? cache.value : undefined,
    actions: actions.status === 'fulfilled' ? actions.value : [],
    errors: {
      overview: overview.status === 'rejected' ? message(overview.reason) : '',
      images: images.status === 'rejected' ? message(images.reason) : '',
      volumes: volumes.status === 'rejected' ? message(volumes.reason) : '',
      'build-cache': cache.status === 'rejected' ? message(cache.reason) : '',
      actions: actions.status === 'rejected' ? message(actions.reason) : '',
    },
  };
}

/** Size reads poll only while some producer is still calculating. */
export const isRefreshing = (d: StorageData) =>
  [d.overview?.images, d.overview?.containers, d.overview?.volumes, d.overview?.buildCache, d.images?.totals, d.volumes?.totals, d.cache?.totals].some((t) => t?.refreshing);

const IMAGE_COLUMNS = [
  { key: 'image', label: 'Image', width: 'minmax(220px, 1.6fr)' },
  { key: 'size', label: 'Size', width: '100px' },
  { key: 'usedBy', label: 'Used by', width: 'minmax(160px, 1.2fr)' },
  { key: 'status', label: 'Status', width: '120px' },
  { key: 'actions', label: 'Actions', width: '90px' },
];
const VOLUME_COLUMNS = [{ ...IMAGE_COLUMNS[0], key: 'name', label: 'Volume' }, ...IMAGE_COLUMNS.slice(1)];
const CACHE_COLUMNS = [
  { key: 'record', label: 'Record', width: 'minmax(220px, 1.6fr)' },
  { key: 'size', label: 'Size', width: '100px' },
  { key: 'lastUsed', label: 'Last used', width: '150px' },
  { key: 'status', label: 'Status', width: '140px' },
  { key: 'actions', label: 'Actions', width: '0px' },
];

const LAYER_COLUMNS = [
  { key: 'container', label: 'Container', width: 'minmax(200px, 1.6fr)' },
  { key: 'stack', label: 'Stack', width: 'minmax(120px, 1fr)' },
  { key: 'size', label: 'Size', width: '100px' },
];

function Lead({ overview }: { overview: StorageOverview }) {
  const summary = usageSummary(overview);
  const reclaimable = reclaimableText(summary);
  return (
    <p className={styles.lead}>
      <span>{usedText(summary)}</span>
      {reclaimable && (
        <>
          {' · '}
          <span className={styles.accent}>{reclaimable}</span>
        </>
      )}
      {' · '}
      {summary.sizesAt ? (
        <>
          sizes from <RelativeTime iso={summary.sizesAt} />
        </>
      ) : (
        'Sizes not available yet'
      )}
      {summary.refreshing && ' · refreshing…'}
    </p>
  );
}


const countText = (count: number, noun: string) => `${count} ${noun}${count === 1 ? '' : 's'}`;
const sortedStatus = (count: number, noun: string) => `${countText(count, noun)} · sorted by size`;

function LayersTable({ overview, status }: { overview: StorageOverview; status: string }) {
  const layers = sortBySize(overview.topContainers, (c) => c.sizeRw);
  return (
    <StorageTable label="Container layers" columns={LAYER_COLUMNS} status={status} sizes={overview.containers} empty={layers.length ? undefined : 'No container layers.'}>
      {layers.map((layer) => (
        <StorageRow key={layer.name}>
          <StorageCell mono>
            <Link to={`/containers/${encodeURIComponent(layer.name)}`}>{layer.name}</Link>
          </StorageCell>
          <StorageCell place="sub">{layer.stack ? <Link to={`/stacks/${encodeURIComponent(layer.stack)}`}>{layer.stack}</Link> : '—'}</StorageCell>
          <StorageCell place="end" mono>
            {sizeText(layer.sizeRw)}
          </StorageCell>
        </StorageRow>
      ))}
    </StorageTable>
  );
}

export function StorageScreen() {
  const [query] = useSearchParams();
  const kind = kindFromQuery(query.get('tab'));
  const navigate = useNavigate();
  const { data, refresh } = useStorageRefresh(loadStorage, isRefreshing);
  const liveState = useStore(live);
  const dockerDown = liveState.docker === 'unavailable' || liveState.events === 'disconnected';
  const recalc = useRecalculate(dockerDown, refresh);
  const [cleanup, setCleanup] = useState<StoragePlanRequest | undefined>();
  const [imageChip, setImageChip] = useState<ImageChip>('all');
  const [volumeChip, setVolumeChip] = useState<VolumeChip>('all');
  const [search, setSearch] = useState('');
  const [page, setPage] = useState(1);

  // Same behaviour as the Svelte tabs: a new kind is a history entry, reselecting the current one does nothing.
  const selectKind = (id: StorageKind) => {
    if (id === kind) return;
    setSearch('');
    setPage(1);
    void navigate(`/storage?tab=${id}`);
  };
  const changeSearch = (value: string) => {
    setSearch(value);
    setPage(1);
  };

  const images = data?.images?.images ?? [];
  const volumes = data?.volumes?.volumes ?? [];
  const records = data?.cache?.records ?? [];
  const shownImages = sortBySize(filterImages(images, imageChip, search), (i) => i.size);
  const shownVolumes = sortBySize(filterVolumes(volumes, volumeChip, search), (v) => v.size);
  const shownCache = sortBySize(records, (r) => r.size);
  const imagePage = pageView(shownImages, page);
  const volumePage = pageView(shownVolumes, page);
  const cachePage = pageView(shownCache, page);
  const kindError = data?.errors[kind === 'layers' ? 'overview' : kind] ?? '';
  const candidates = images.filter((image) => image.dangling && !image.containers.length);
  const imagesFailed = Boolean(data?.errors.images);
  // A failed list has no candidates, so "(0 · …)" would be a false count.
  const danglingLabel = imagesFailed ? 'Clean up dangling images' : `Clean up dangling images (${candidates.length} · ${imageReclaimRange(candidates)})`;
  const unknown = data?.overview?.declarationsUnknown ?? [];
  // One region for the whole screen: it stays mounted across kind changes so the new selection is announced as a text change.
  const status: Record<StorageKind, string> = {
    layers: sortedStatus(data?.overview?.topContainers.length ?? 0, 'container layer'),
    images: sortedStatus(shownImages.length, 'image'),
    volumes: sortedStatus(shownVolumes.length, 'volume'),
    'build-cache': sortedStatus(shownCache.length, 'record'),
  };
  const loaded = { layers: data?.overview, images: data?.images, volumes: data?.volumes, 'build-cache': data?.cache }[kind];
  const statusText = loaded ? status[kind] : '';

  return (
    <section className={styles.page} aria-labelledby="storage-heading">
      <header className={styles.head}>
        <div className={styles.headText}>
          <h1 id="storage-heading" className={styles.title}>
            Storage
          </h1>
          {data?.overview && <Lead overview={data.overview} />}
        </div>
        <div className={styles.refresh}>
          <Button variant="secondary" disabledReason={recalc.reason} onClick={() => void recalc.recalculate()}>
            Recalculate
          </Button>
        </div>
      </header>
      <p className="visually-hidden" role="status" aria-label="Selected storage kind">
        {statusText}
      </p>
      {recalc.error && (
        <Banner kind="warning" title="Could not recalculate">
          {recalc.error}
        </Banner>
      )}
      {!data ? (
        <Spinner label="Loading storage" />
      ) : (
        <>
          {unknown.length > 0 && (
            <Banner kind="warning" title={`Declarations unknown for: ${unknown.join(', ')}`}>
              Declared volumes remain protected where ownership is known. Re-check the stack configuration.
            </Banner>
          )}
          {data.overview ? (
            <>
              <UsageCard overview={data.overview} selected={kind} dockerDown={dockerDown} danglingLabel={danglingLabel} danglingUnavailable={imagesFailed ? 'The image list could not be loaded' : undefined} onSelect={selectKind} onCleanup={setCleanup} />
              {kind === 'layers' && <LayersTable overview={data.overview} status={status.layers} />}
            </>
          ) : (
            <div className={styles.retry}>
              <Banner kind="danger" title="Could not load storage">
                {data.errors.overview}
              </Banner>
              <Button variant="secondary" onClick={refresh}>
                Retry
              </Button>
            </div>
          )}
          {kind === 'images' && (
            <StorageTable
              label="Images"
              columns={IMAGE_COLUMNS}
              status={status.images}
              sizes={data.images?.totals}
              failure={kindError ? { message: kindError, onRetry: refresh } : undefined}
              toolbar={
                <KindToolbar
                  chips={imageChips}
                  counts={imageCounts(images)}
                  pressed={imageChip}
                  onChip={(id) => {
                    setImageChip(id as ImageChip);
                    setPage(1);
                  }}
                  groupLabel="Filter images"
                  searchLabel="Search images"
                  search={search}
                  onSearch={changeSearch}
                />
              }
              empty={shownImages.length ? undefined : 'No images match the current filter.'}
              footer={<Pager {...imagePage} label="Images" onPage={setPage} />}
            >
              {data.images && <ImageRows page={imagePage} totals={data.images.totals} dockerDown={dockerDown} onDelete={(image) => setCleanup({ kind: 'image', ref: image.id })} />}
            </StorageTable>
          )}
          {kind === 'volumes' && (
            <StorageTable
              label="Volumes"
              columns={VOLUME_COLUMNS}
              status={status.volumes}
              sizes={data.volumes?.totals}
              failure={kindError ? { message: kindError, onRetry: refresh } : undefined}
              toolbar={
                <KindToolbar
                  chips={volumeChips}
                  counts={volumeCounts(volumes)}
                  pressed={volumeChip}
                  onChip={(id) => {
                    setVolumeChip(id as VolumeChip);
                    setPage(1);
                  }}
                  groupLabel="Filter volumes"
                  searchLabel="Search volumes"
                  search={search}
                  onSearch={changeSearch}
                />
              }
              empty={shownVolumes.length ? undefined : 'No volumes match the current filter.'}
              footer={<Pager {...volumePage} label="Volumes" onPage={setPage} />}
            >
              <VolumeRows page={volumePage} dockerDown={dockerDown} onDelete={(volume) => setCleanup({ kind: 'volume', name: volume.name })} />
            </StorageTable>
          )}
          {kind === 'build-cache' && (
            <StorageTable
              label="Build cache"
              columns={CACHE_COLUMNS}
              status={status['build-cache']}
              sizes={data.cache?.totals}
              failure={kindError ? { message: kindError, onRetry: refresh } : undefined}
              empty={shownCache.length ? undefined : 'No build cache records.'}
              footer={<Pager {...cachePage} label="Build cache" onPage={setPage} />}
            >
              {data.cache && <CacheRows page={cachePage} totals={data.cache.totals} />}
            </StorageTable>
          )}
          <RecentActions actions={data.actions} error={data.errors.actions} />
        </>
      )}
      {cleanup && <CleanupDialog request={cleanup} onClose={() => setCleanup(undefined)} onFinished={refresh} />}
    </section>
  );
}
