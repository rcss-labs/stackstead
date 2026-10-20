import { Link } from 'react-router';
import type { StorageVolume } from '$lib/api/types';
import { deleteDisabledReason, sizeText, type PageView } from '$lib/storage/storage';
import { Button } from '../components/Button';
import { Tag } from '../components/Tag';
import styles from './StorageTable.module.css';
import { StorageCell, StorageRow } from './StorageTable';
import { UsedBy } from './UsedBy';

const statusText = (volume: StorageVolume) => (volume.containers.length ? 'in use' : volume.protection ? 'unused · protected' : 'unused');

function ProtectionNote({ volume }: { volume: StorageVolume }) {
  if (!volume.protection) return null;
  const stack = volume.protection.stack;
  const declaration = volume.declaredBy.find((d) => d.stack === stack);
  return (
    <p className={styles.protection}>
      {declaration ? 'declared by stack' : 'protected by stack'} <Link to={`/stacks/${encodeURIComponent(stack)}`}>{stack}</Link>
      {declaration && ` (${declaration.stackState})`} — remove via the stack
    </p>
  );
}

export function VolumeRows({ page, dockerDown, onDelete }: { page: PageView<StorageVolume>; dockerDown: boolean; onDelete: (volume: StorageVolume) => void }) {
  return (
    <>
      {page.rows.map((volume) => {
        const status = statusText(volume);
        const firstUser = volume.containers[0]?.name ?? volume.declaredBy[0]?.stack ?? '—';
        return (
          <StorageRow key={volume.name}>
            <StorageCell>
              <Link className={styles.mono} to={`/storage/volumes/${encodeURIComponent(volume.name)}`}>
                {volume.name}
              </Link>
              <span className={styles.sub}>{`${status} · ${firstUser}`}</span>
              <div className={styles.driver}>{volume.driver}</div>
            </StorageCell>
            <StorageCell place="end" mono>
              {sizeText(volume.size, volume.sizesPending)}
            </StorageCell>
            <StorageCell place="desktop">
              <UsedBy consumers={volume.containers} declarations={volume.declaredBy}>
                <ProtectionNote volume={volume} />
              </UsedBy>
            </StorageCell>
            <StorageCell place="desktop">
              <Tag tone={status === 'unused' ? 'warning' : 'muted'}>{status}</Tag>
            </StorageCell>
            <StorageCell place="action">
              {!volume.protection && !volume.containers.length && (
                <Button variant="danger" size="sm" disabledReason={deleteDisabledReason(volume, dockerDown)} onClick={() => onDelete(volume)}>
                  Delete
                </Button>
              )}
            </StorageCell>
          </StorageRow>
        );
      })}
    </>
  );
}
