import type { StorageBuildRecord, StorageKindTotals } from '$lib/api/types';
import { knownTime, sizeText, type PageView } from '$lib/storage/storage';
import { RelativeTime } from '../components/RelativeTime';
import { Tag } from '../components/Tag';
import styles from './StorageTable.module.css';
import { StorageCell, StorageRow } from './StorageTable';

export function CacheRows({ page, totals }: { page: PageView<StorageBuildRecord>; totals: StorageKindTotals }) {
  return (
    <>
      {page.rows.map((record) => (
        <StorageRow key={record.id}>
          <StorageCell>
            <span className={styles.clamp} title={record.description}>
              {record.description}
            </span>
            <span className={styles.sub}>{record.inUse ? 'in use' : 'unused'}</span>
            <div className={`${styles.mono} ${styles.muted}`}>{`${record.type} · ${record.id}`}</div>
          </StorageCell>
          <StorageCell place="end" mono>
            {sizeText(record.size, totals.refreshing)}
          </StorageCell>
          <StorageCell place="desktop">{knownTime(record.lastUsedAt) ? <>Last used <RelativeTime iso={record.lastUsedAt!} /></> : '—'}</StorageCell>
          <StorageCell place="desktop">
            <Tag tone="muted">{record.inUse ? 'in use' : 'unused'}</Tag>
            {record.shared && <Tag tone="muted">shared</Tag>}
          </StorageCell>
          <StorageCell place="action">{null}</StorageCell>
        </StorageRow>
      ))}
    </>
  );
}
