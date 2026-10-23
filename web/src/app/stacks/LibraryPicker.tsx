import { useEffect, useId, useRef, useState } from 'react';
import { Link } from 'react-router';
import type { Library, LibraryImage, LibraryImageDetail, LibraryVersion } from '$lib/api/imageTypes';
import { groupByOwner } from '$lib/images/library';
import { formatLibraryRef, parseLibraryRef } from '$lib/images/ref';
import { useServer } from '../servers/ServerContext';
import styles from './LibraryPicker.module.css';

export interface LibraryPickerProps {
  /** The chosen library reference, or ''. */
  value: string;
  onChange(ref: string): void;
  load(): Promise<Library>;
  /** The image's detail, from the server that owns it; its `versions` are what can be chosen. */
  loadImage(image: LibraryImage): Promise<LibraryImageDetail>;
  /** This server's architecture; versions built for another one cannot be chosen. */
  localArch?: string;
}

const OWNER_LEN = 12;

/** The library list carries each image's latest version; only a successful build can be run. */
function runnable(image: LibraryImage) {
  return image.latestVersion?.status === 'succeeded' ? image.latestVersion : undefined;
}

export function LibraryPicker({ value, onChange, load, loadImage, localArch }: LibraryPickerProps) {
  const { href } = useServer();
  const radioName = useId();
  const [library, setLibrary] = useState<Library | undefined>();
  const [failed, setFailed] = useState(false);
  const [picked, setPicked] = useState<string | undefined>();
  const [detail, setDetail] = useState<{ key: string; versions: LibraryVersion[] } | undefined>();
  const [detailFailed, setDetailFailed] = useState(false);
  const loadImageRef = useRef(loadImage);
  loadImageRef.current = loadImage;
  const parsed = parseLibraryRef(value);
  const ownerOf = (image: LibraryImage) => image.owner.id.slice(0, OWNER_LEN);
  const keyOf = (image: LibraryImage) => `${ownerOf(image)}/${image.slug}`;
  // The image shown as chosen: the one clicked, else the one the current reference points at.
  const selectedKey = picked ?? (parsed ? `${parsed.owner}/${parsed.slug}` : undefined);
  const selected = library?.images.find((image) => keyOf(image) === selectedKey);

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

  useEffect(() => {
    if (!selected) return;
    let alive = true;
    const key = keyOf(selected);
    setDetailFailed(false);
    loadImageRef.current(selected).then(
      (d) => {
        if (!alive) return;
        const versions = d.versions
          .filter((v) => v.status === 'succeeded')
          .sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt));
        setDetail({ key, versions });
      },
      () => alive && setDetailFailed(true),
    );
    return () => {
      alive = false;
    };
    // keyed by the image only; the library list object is stable once loaded.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedKey, library]);

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
  const versions = detail && detail.key === selectedKey ? detail.versions : undefined;
  const archNote = (v: LibraryVersion) => (localArch && v.arch && v.arch !== localArch ? `built for ${v.arch}; this server is ${localArch}` : '');
  const versionRef = (image: LibraryImage, v: LibraryVersion) => formatLibraryRef(ownerOf(image), image.slug, v.tag);

  function chooseImage(image: LibraryImage) {
    setPicked(keyOf(image));
    // Until the versions arrive, the latest one is the default.
    const latest = runnable(image);
    if (latest) onChange(versionRef(image, latest));
  }

  return (
    <div className={styles.picker}>
      {groups.map(({ owner, images }) => (
        <fieldset key={owner.id} className={styles.owner}>
          <legend className={styles.ownerName}>{owner.name}</legend>
          {images.map((image) => (
            <label key={keyOf(image)} className={styles.option}>
              <input type="radio" name={`${radioName}-image`} value={keyOf(image)} checked={keyOf(image) === selectedKey} onChange={() => chooseImage(image)} />
              <span>{image.slug}</span>
            </label>
          ))}
        </fieldset>
      ))}
      {selected && detailFailed && (
        <p className={styles.error} role="alert">
          Could not load the versions of {selected.slug}.
        </p>
      )}
      {selected && versions && (
        <fieldset className={styles.owner}>
          <legend className={styles.ownerName}>Version</legend>
          {versions.map((v) => {
            const ref = versionRef(selected, v);
            const note = archNote(v);
            return (
              <label key={v.id} className={styles.option}>
                <input type="radio" name={`${radioName}-version`} value={ref} checked={value === ref} disabled={note !== ''} onChange={() => onChange(ref)} />
                <span>{v.tag}</span>
                {note && <span className={styles.note}>{note}</span>}
              </label>
            );
          })}
        </fieldset>
      )}
      {value && (!selected || (versions && !versions.some((v) => versionRef(selected, v) === value))) && <p className={styles.current}>Current: {value}</p>}
    </div>
  );
}
