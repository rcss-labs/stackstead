import { useEffect, useId, useState } from 'react';
import { Link } from 'react-router';
import type { Library, LibraryImage } from '$lib/api/imageTypes';
import { groupByOwner } from '$lib/images/library';
import { formatLibraryRef } from '$lib/images/ref';
import { useServer } from '../servers/ServerContext';
import styles from './LibraryPicker.module.css';

export interface LibraryPickerProps {
  /** The chosen library reference, or ''. */
  value: string;
  onChange(ref: string): void;
  load(): Promise<Library>;
  /** This server's architecture; versions built for another one cannot be chosen. */
  localArch?: string;
}

/** The library list carries each image's latest version; only a successful build can be run. */
function runnable(image: LibraryImage) {
  return image.latestVersion?.status === 'succeeded' ? image.latestVersion : undefined;
}

export function LibraryPicker({ value, onChange, load, localArch }: LibraryPickerProps) {
  const { href } = useServer();
  const radioName = useId();
  const [library, setLibrary] = useState<Library | undefined>();
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let alive = true;
    load().then(
      (lib) => alive && setLibrary(lib),
      () => alive && setFailed(true),
    );
    return () => {
      alive = false;
    };
    // load is a fresh closure on every render of the parent; the list is fetched once per mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (failed) {
    return (
      <p className={styles.error} role="alert">
        Could not load the image library.
      </p>
    );
  }
  if (!library) return <p className={styles.empty}>Loading images…</p>;
  const groups = groupByOwner(library.images.filter(runnable));
  if (groups.length === 0) {
    return (
      <p className={styles.empty}>
        <span>No images yet</span>. <Link to={href('/images')}>Open the Images screen</Link> to build one.
      </p>
    );
  }
  const chosen = groups.some((g) => g.images.some((image) => formatLibraryRef(image.owner.id.slice(0, 12), image.slug, runnable(image)!.tag) === value));

  return (
    <div className={styles.picker}>
      {groups.map(({ owner, images }) => (
        <fieldset key={owner.id} className={styles.owner}>
          <legend className={styles.ownerName}>{owner.name}</legend>
          {images.map((image) => {
            const version = runnable(image)!;
            const ref = formatLibraryRef(owner.id.slice(0, 12), image.slug, version.tag);
            const mismatch = Boolean(localArch && version.arch && version.arch !== localArch);
            return (
              <label key={image.slug} className={styles.option}>
                <input type="radio" name={radioName} value={ref} checked={value === ref} disabled={mismatch} onChange={() => onChange(ref)} />
                <span>
                  {image.slug}:{version.tag}
                </span>
                {mismatch && <span className={styles.note}>{`built for ${version.arch}; this server is ${localArch}`}</span>}
              </label>
            );
          })}
        </fieldset>
      ))}
      {value && !chosen && <p className={styles.current}>Current: {value}</p>}
    </div>
  );
}
