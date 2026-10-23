import type { LibraryUse } from '$lib/api/imageTypes';
import { ApiError } from '$lib/api/client';
import { Banner } from '../components/Banner';
import styles from './images.module.css';

/** The services a refused delete (409 image_in_use) lists, from the error body's `usedBy`. */
export function inUseFrom(e: unknown): { message: string; usedBy: LibraryUse[] } | undefined {
  if (!(e instanceof ApiError) || e.status !== 409 || e.code !== 'image_in_use') return undefined;
  const usedBy = (e.body as { usedBy?: LibraryUse[] } | undefined)?.usedBy;
  return { message: e.message, usedBy: Array.isArray(usedBy) ? usedBy : [] };
}

export function InUseBanner({ title, message, usedBy }: { title: string; message: string; usedBy: LibraryUse[] }) {
  return (
    <Banner kind="danger" title={title}>
      <p>{message}</p>
      {usedBy.length > 0 && (
        <ul className={styles.usedBy}>
          {usedBy.map((use) => (
            <li key={`${use.server}/${use.stack}/${use.service}`}>
              {use.stack} / {use.service}
            </li>
          ))}
        </ul>
      )}
    </Banner>
  );
}
