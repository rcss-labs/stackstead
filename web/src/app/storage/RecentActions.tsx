import type { ActionEntry } from '$lib/api/types';
import { sizeText } from '$lib/storage/storage';
import { Banner } from '../components/Banner';
import { RelativeTime } from '../components/RelativeTime';
import styles from './StorageScreen.module.css';
import { StorageCell, StorageRow, StorageTable } from './StorageTable';

const COLUMNS = [
  { key: 'time', label: 'Time', width: '110px' },
  { key: 'user', label: 'User', width: 'minmax(80px, 0.6fr)' },
  { key: 'kind', label: 'Kind', width: '130px' },
  { key: 'ref', label: 'Ref', width: 'minmax(140px, 1.4fr)' },
  { key: 'result', label: 'Result', width: '100px' },
  { key: 'bytes', label: 'Bytes', width: '90px' },
];

const HEADING_ID = 'storage-actions-heading';

export function RecentActions({ actions, error }: { actions: ActionEntry[]; error: string }) {
  return (
    <section aria-labelledby={HEADING_ID} className={styles.section}>
      {/* With rows the table title is the section heading; the section heading only stands in when there is no table. */}
      {(error || actions.length === 0) && (
        <h2 id={HEADING_ID} className={styles.sectionTitle}>
          Recent storage actions
        </h2>
      )}
      {error ? (
        <Banner kind="warning" title="Could not load recent actions">
          {error}
        </Banner>
      ) : actions.length === 0 ? (
        <p className={styles.muted}>No storage actions yet.</p>
      ) : (
        <StorageTable label="Recent storage actions" titleId={HEADING_ID} columns={COLUMNS} status={`${actions.length} actions`}>
          {actions.slice(0, 20).map((action) => (
            <StorageRow key={action.id}>
              <StorageCell place="sub">
                <RelativeTime iso={action.ts} />
              </StorageCell>
              <StorageCell place="sub">{action.username}</StorageCell>
              <StorageCell place="sub" mono>
                {action.kind}
              </StorageCell>
              <StorageCell mono title={action.detail}>
                {action.target}
              </StorageCell>
              <StorageCell place="sub">{action.result}</StorageCell>
              <StorageCell place="end" mono>
                {sizeText(action.bytes)}
              </StorageCell>
            </StorageRow>
          ))}
        </StorageTable>
      )}
    </section>
  );
}
