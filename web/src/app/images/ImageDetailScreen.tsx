import { useEffect, useState } from 'react';
import { Link, useParams, useSearchParams } from 'react-router';
import type { BuildStarted } from '$lib/api/imageTypes';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { CopyButton } from '../components/CopyButton';
import { EmptyState } from '../components/EmptyState';
import { Spinner } from '../components/Spinner';
import { Tabs } from '../components/Tabs';
import { Tag } from '../components/Tag';
import { OperationPanel } from '../detail/OperationPanel';
import { useServer } from '../servers/ServerContext';
import { BuildDialog } from './BuildDialog';
import styles from './images.module.css';
import { ImageSettingsTab } from './ImageSettingsTab';
import { RevisionsTab } from './RevisionsTab';
import { SourceTab } from './SourceTab';
import { useImageDetail } from './useImageDetail';
import { VersionsTab } from './VersionsTab';

const ALL_TABS = [
  { id: 'source', label: 'Source' },
  { id: 'revisions', label: 'Revisions' },
  { id: 'versions', label: 'Versions' },
  { id: 'settings', label: 'Settings' },
];

/** GitHub images have no stored revisions: their source lives in the repository. */
const tabsFor = (source: string) => (source === 'github' ? ALL_TABS.filter((t) => t.id !== 'revisions') : ALL_TABS);

/** Keyed on the slug so /images/a to /images/b restarts every load. */
export function ImageDetailScreen() {
  const { slug = '' } = useParams();
  return <ImageDetail key={slug} slug={slug} />;
}

function ImageDetail({ slug }: { slug: string }) {
  const { href, trackOperation, refreshRunningOps, api } = useServer();
  const { detail, error, notFound, reload } = useImageDetail(slug);
  const [query, setQuery] = useSearchParams();
  const requested = query.get('tab');
  const [building, setBuilding] = useState(false);
  const [buildOperation, setBuildOperation] = useState<string>();

  useEffect(() => {
    document.title = `${detail?.name ?? slug} · Images · Stackstead`;
  }, [detail?.name, slug]);

  function started({ operation }: BuildStarted) {
    setBuilding(false);
    setBuildOperation(operation.id);
    trackOperation(operation);
    refreshRunningOps();
    reload();
  }

  if (notFound) {
    return (
      <div className={styles.card}>
        <EmptyState title="Image not found" description={`There is no image "${slug}" on this server.`}>
          <Link to={href('/images')}>Back to images</Link>
        </EmptyState>
      </div>
    );
  }
  if (!detail) {
    return error ? (
      <Banner kind="danger" title="Could not load the image">
        {error.message}{' '}
        <Button variant="secondary" size="sm" onClick={reload}>
          Retry
        </Button>
      </Banner>
    ) : (
      <Spinner />
    );
  }

  const tabs = tabsFor(detail.source);
  const tab = tabs.some((t) => t.id === requested) ? (requested as string) : 'source';
  const latest = detail.latestVersion;
  return (
    <section className={styles.page} aria-labelledby="image-heading">
      <header className={styles.head}>
        <h1 id="image-heading" className={styles.title}>
          {detail.name}
        </h1>
        <Tag tone="muted">{detail.source}</Tag>
        {!detail.owner.local && <Tag tone="info">{detail.owner.name}</Tag>}
        <span className={styles.spacer} />
        <Button variant="primary" onClick={() => setBuilding(true)}>
          Build
        </Button>
      </header>
      <p className={styles.meta}>
        {latest ? (
          <>
            <span className={styles.ref}>{latest.ref}</span>
            <CopyButton text={latest.ref} />
          </>
        ) : (
          <span>No version built yet</span>
        )}
      </p>

      {buildOperation && (
        <OperationPanel
          key={buildOperation}
          operationId={buildOperation}
          onCancel={async (id) => {
            await api.cancel(id);
          }}
          onFinished={reload}
        />
      )}

      <Tabs tabs={tabs} selected={tab} ariaLabel="Image sections" onSelect={(id) => setQuery({ tab: id })}>
        {(selected) => (
          <>
            {selected === 'source' && <SourceTab image={detail} onChanged={reload} />}
            {selected === 'revisions' && <RevisionsTab image={detail} onChanged={reload} />}
            {selected === 'versions' && <VersionsTab image={detail} onChanged={reload} />}
            {selected === 'settings' && <ImageSettingsTab image={detail} onChanged={reload} />}
          </>
        )}
      </Tabs>

      {building && <BuildDialog image={detail} onStarted={started} onClose={() => setBuilding(false)} />}
    </section>
  );
}
