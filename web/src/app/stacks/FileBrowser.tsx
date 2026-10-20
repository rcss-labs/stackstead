import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, ApiError } from '$lib/api/client';
import type { FSEntry } from '$lib/api/types';
import { Button } from '../components/Button';
import { Dialog } from '../components/Dialog';
import { Spinner } from '../components/Spinner';
import styles from './FileBrowser.module.css';

export interface FileBrowserProps {
  open: boolean;
  mode: 'dir' | 'file';
  title: string;
  onPick: (path: string) => void;
  onClose: () => void;
  /** Files for which this returns true are listed first; others are still shown. */
  filter?: (e: FSEntry) => boolean;
}

const isDockerfile = (e: FSEntry) => /^Dockerfile/.test(e.name) || /\.Dockerfile$/.test(e.name);
const join = (dir: string, name: string) => (dir.endsWith('/') ? dir + name : `${dir}/${name}`);

function describe(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 403) {
      return /permission/i.test(err.message) ? 'Permission denied: this directory cannot be read.' : 'This path is outside the allowed directories.';
    }
    if (err.status === 404) return 'No such directory.';
    return err.message;
  }
  return 'Could not list this directory.';
}

export function FileBrowser({ open, mode, title, onPick, onClose, filter }: FileBrowserProps) {
  const preferred = filter ?? isDockerfile;
  // path '' is the list of allowed roots; root is the allowed root containing path.
  const [path, setPath] = useState('');
  const [root, setRoot] = useState('');
  const [entries, setEntries] = useState<FSEntry[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [announcement, setAnnouncement] = useState('');
  // Bumped once per finished load so the effect below moves focus onto the new list.
  const [loaded, setLoaded] = useState(0);
  const seq = useRef(0);
  const listRef = useRef<HTMLDivElement>(null);

  const load = useCallback(async (next: string, nextRoot: string) => {
    const mine = ++seq.current;
    setLoading(true);
    setError('');
    let list: FSEntry[] = [];
    let message = '';
    try {
      list = await api.fs(next || undefined);
    } catch (err) {
      message = describe(err);
    }
    if (mine !== seq.current) return;
    setEntries(list);
    setError(message);
    setPath(next);
    setRoot(nextRoot);
    setAnnouncement(`${next || 'Allowed directories'}: ${message || `${list.length} entries`}`);
    setLoading(false);
    setLoaded((n) => n + 1);
  }, []);

  useEffect(() => {
    if (open) void load('', '');
    else seq.current++;
  }, [open, load]);

  // The list is replaced after the fetch, so move focus to the new content (first enabled entry, else the list region).
  useEffect(() => {
    if (!loaded) return;
    const list = listRef.current;
    (list?.querySelector<HTMLButtonElement>('button:not([disabled])') ?? list)?.focus();
  }, [loaded]);

  const crumbs = useMemo(() => {
    if (!path) return [];
    const out = [{ label: root, path: root }];
    let acc = root;
    for (const part of path.slice(root.length).split('/').filter(Boolean)) {
      acc = join(acc, part);
      out.push({ label: part, path: acc });
    }
    return out;
  }, [path, root]);

  const sorted = useMemo(() => {
    const dirs = entries.filter((e) => e.isDir);
    const files = entries.filter((e) => !e.isDir);
    if (mode === 'dir') return [...dirs, ...files];
    return [...dirs, ...files.filter((e) => preferred(e)), ...files.filter((e) => !preferred(e))];
  }, [entries, mode, preferred]);

  function choose(e: FSEntry) {
    if (e.isDir) {
      void load(path ? join(path, e.name) : e.name, path ? root : e.name);
    } else if (mode === 'file') {
      onPick(join(path, e.name));
    }
  }

  return (
    <Dialog open={open} title={title} onClose={onClose}>
      <nav aria-label="Current location" className={styles.crumbs}>
        {path ? (
          <>
            <button type="button" className={styles.crumb} onClick={() => void load('', '')}>
              Allowed directories
            </button>
            {crumbs.map((crumb, i) => (
              <span key={crumb.path} className={styles.step}>
                <span aria-hidden="true">›</span>
                {i === crumbs.length - 1 ? (
                  <span aria-current="location" className={styles.here}>
                    {crumb.label}
                  </span>
                ) : (
                  <button type="button" className={styles.crumb} onClick={() => void load(crumb.path, root)}>
                    {crumb.label}
                  </button>
                )}
              </span>
            ))}
          </>
        ) : (
          <span aria-current="location" className={styles.here}>
            Allowed directories
          </span>
        )}
      </nav>

      <p role="status" className="visually-hidden">
        {announcement}
      </p>
      {loading && <Spinner />}
      <div ref={listRef} tabIndex={-1} aria-busy={loading} className={styles.list}>
        {error ? (
          <p role="alert" className={styles.error}>
            <span aria-hidden="true">✖ </span>
            {error}
          </p>
        ) : sorted.length === 0 && !loading ? (
          <p className={styles.muted}>This directory is empty.</p>
        ) : (
          <ul className={styles.entries}>
            {sorted.map((e, i) => (
              <li key={i}>
                <button type="button" className={styles.entry} disabled={!e.isDir && mode === 'dir'} onClick={() => choose(e)}>
                  <span aria-hidden="true">{e.isDir ? '📁' : '📄'} </span>
                  {e.name}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>

      <div className={styles.actions}>
        <Button variant="secondary" onClick={onClose}>
          Cancel
        </Button>
        {mode === 'dir' && path && (
          <Button variant="primary" disabled={!!error || loading} onClick={() => onPick(path)}>
            Use this directory
          </Button>
        )}
      </div>
    </Dialog>
  );
}
