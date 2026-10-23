import { useEffect, useId, useRef, useState } from 'react';
import { Link } from 'react-router';
import type { Library, LibraryImage, LibraryImageDetail, LibraryVersion } from '$lib/api/imageTypes';
import { groupByOwner, ownerIsViewed } from '$lib/images/library';
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
  const { href, id: serverId } = useServer();
  const radioName = useId();
  const [library, setLibrary] = useState<Library | undefined>();
  const [failed, setFailed] = useState(false);
  const [picked, setPicked] = useState<string | undefined>();
  const [detail, setDetail] = useState<{ key: string; versions: LibraryVersion[] } | undefined>();
  const [detailFailed, setDetailFailed] = useState(false);
  const loadImageRef = useRef(loadImage);
  loadImageRef.current = loadImage;
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;
  const valueRef = useRef(value);
  valueRef.current = value;
  // While the server's architecture is unknown (status not loaded) a version with a known architecture cannot be chosen yet.
  const archNote = (v: LibraryVersion) => {
    if (!v.arch) return '';
    if (!localArch) return 'checking server architecture…';
    return v.arch !== localArch ? `built for ${v.arch}; this server is ${localArch}` : '';
  };
  const incompatible = (v: LibraryVersion) => archNote(v) !== '';
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

  // A reference changed from outside (another source, a reload) takes over from the image clicked here.
  useEffect(() => {
    if (parsed && picked !== undefined && `${parsed.owner}/${parsed.slug}` !== picked) setPicked(undefined);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value]);

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

  // Keep a reference that already points at a version of this image (a tag that is gone shows as "Current"),
  // unless that version is for the wrong architecture. Otherwise default to the newest compatible version, or
  // clear the choice when there is none, so a version that cannot run here is never selected.
  // Waits for this server's architecture when a version's compatibility depends on it.
  useEffect(() => {
    if (!selected || !detail || detail.key !== selectedKey) return;
    const { versions } = detail;
    if (!localArch && versions.some((v) => v.arch)) return;
    const current = parseLibraryRef(valueRef.current);
    const own = current && `${current.owner}/${current.slug}` === detail.key ? current : undefined;
    const chosen = own && versions.find((v) => v.tag === own.tag);
    if (own && (!chosen || !incompatible(chosen))) return;
    const best = versions.find((v) => !incompatible(v));
    onChangeRef.current(best ? formatLibraryRef(selected.owner.id.slice(0, OWNER_LEN), selected.slug, best.tag) : '');
    // runs when the versions or the architecture arrive; later reference changes are the user's own choice.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [detail, localArch]);

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
  const versionRef = (image: LibraryImage, v: LibraryVersion) => formatLibraryRef(ownerOf(image), image.slug, v.tag);

  function chooseImage(image: LibraryImage) {
    // The version is chosen once the image's versions arrive (see the detail effect).
    setPicked(keyOf(image));
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
      {selected && !ownerIsViewed(selected, serverId) && (
        <p className={styles.note}>
          {selected.owner.name} owns this image; this server fetches the version when you save.
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
