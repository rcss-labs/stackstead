import { Link } from 'react-router';
import type { StorageImage, StorageKindTotals } from '$lib/api/types';
import { formatBytes } from '$lib/format';
import { deleteDisabledReason, imagePath, sizeText, type PageView } from '$lib/storage/storage';
import { Button } from '../components/Button';
import { RelativeTime } from '../components/RelativeTime';
import { Tag } from '../components/Tag';
import styles from './StorageTable.module.css';
import { StorageCell, StorageRow } from './StorageTable';
import { UsedBy } from './UsedBy';

const shortId = (id: string) => id.replace(/^sha256:/, '').slice(0, 12);
export const knownTime = (iso: string | null) => Boolean(iso) && !iso!.startsWith('0001-');

const statusText = (image: StorageImage) => (image.containers.length ? 'in use' : image.dangling ? 'dangling' : 'no containers');

export function ImageRows({ page, totals, dockerDown, onDelete }: { page: PageView<StorageImage>; totals: StorageKindTotals; dockerDown: boolean; onDelete: (image: StorageImage) => void }) {
  return (
    <>
      {page.rows.map((image) => {
        const path = imagePath(image.id);
        const status = statusText(image);
        const firstUser = image.containers[0]?.name ?? image.declaredBy[0]?.stack ?? '—';
        const unique = image.size !== null && image.sharedSize !== null ? `unique ≈ ${formatBytes(Math.max(0, image.size - image.sharedSize))}` : undefined;
        return (
          <StorageRow key={image.id}>
            <StorageCell>
              <div className={styles.refs}>
                {image.tags.length ? (
                  image.tags.map((tag) =>
                    path ? (
                      <Link key={tag} className={styles.tag} to={path}>
                        {tag}
                      </Link>
                    ) : (
                      <span key={tag} className={styles.tag}>
                        {tag}
                      </span>
                    ),
                  )
                ) : path ? (
                  <Link className={styles.mono} to={path}>{`<none> ${shortId(image.id)}`}</Link>
                ) : (
                  <span className={styles.mono}>{`<none> ${shortId(image.id)}`}</span>
                )}
              </div>
              <span className={styles.sub}>{`${status} · ${firstUser}`}</span>
              <div className={styles.created}>{knownTime(image.created) ? <>Created <RelativeTime iso={image.created} /></> : '—'}</div>
            </StorageCell>
            <StorageCell place="end" mono title={unique}>
              {sizeText(image.size, totals.refreshing)}
            </StorageCell>
            <StorageCell place="desktop">
              <UsedBy consumers={image.containers} declarations={image.declaredBy} />
            </StorageCell>
            <StorageCell place="desktop">
              <Tag tone={image.dangling ? 'warning' : 'muted'}>{status}</Tag>
            </StorageCell>
            <StorageCell place="action">
              <Button variant="danger" size="sm" disabledReason={deleteDisabledReason(image, dockerDown)} onClick={() => onDelete(image)}>
                Delete
              </Button>
            </StorageCell>
          </StorageRow>
        );
      })}
    </>
  );
}
