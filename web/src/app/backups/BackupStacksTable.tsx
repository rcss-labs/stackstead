import { Fragment, useCallback, useEffect, useState } from 'react';
import type { BackupsApi } from '$lib/api/backups';
import type { BackupStack, BackupStackSettings } from '$lib/api/backupTypes';
import { formatBytes } from '$lib/format';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { EmptyState } from '../components/EmptyState';
import { Spinner } from '../components/Spinner';
import styles from './backups.module.css';

const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** What each stack contributes to a backup; every toggle saves at once. */
export function BackupStacksTable({ api }: { api: BackupsApi }) {
  const [rows, setRows] = useState<BackupStack[]>();
  const [error, setError] = useState<string>();
  const [open, setOpen] = useState<Set<string>>(new Set());

  const load = useCallback(() => {
    api.stacks().then(setRows, (e: unknown) => setError(messageOf(e)));
  }, [api]);
  useEffect(load, [load]);

  async function toggle(row: BackupStack, change: Partial<BackupStackSettings>) {
    const next: BackupStackSettings = {
      volumes: row.volumes,
      images: row.images,
      stopDuring: row.stopDuring,
      ...change,
    };
    try {
      await api.setStack(row.stack, next);
      setRows((current) => current?.map((r) => (r.stack === row.stack ? { ...r, ...next } : r)));
    } catch (e) {
      toasts.push({ kind: 'danger', message: `Could not save ${row.stack}: ${messageOf(e)}` });
    }
  }

  function toggleOpen(stack: string) {
    setOpen((current) => {
      const next = new Set(current);
      if (!next.delete(stack)) next.add(stack);
      return next;
    });
  }

  if (error)
    return (
      <Banner kind="danger" title="Stacks">
        {error}
      </Banner>
    );
  if (!rows) return <Spinner />;
  if (rows.length === 0) return <EmptyState title="No stacks" description="Stacks you create show up here." />;

  return (
    <div className={styles.scroll}>
      <table className={styles.table}>
        <thead>
          <tr>
            <th scope="col">Stack</th>
            <th scope="col">Volumes</th>
            <th scope="col">Images</th>
            <th scope="col">Stop while backing up</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <Fragment key={row.stack}>
              <tr>
                <td>
                  <button
                    type="button"
                    className={styles.expand}
                    aria-expanded={open.has(row.stack)}
                    aria-label={`Show mounts of ${row.stack}`}
                    onClick={() => toggleOpen(row.stack)}
                  >
                    {open.has(row.stack) ? '▾' : '▸'} {row.stack}
                  </button>
                </td>
                <td>
                  <input type="checkbox" aria-label={`Volumes ${row.stack}`} checked={row.volumes} onChange={(e) => void toggle(row, { volumes: e.target.checked })} />
                </td>
                <td>
                  <input type="checkbox" aria-label={`Images ${row.stack}`} checked={row.images} onChange={(e) => void toggle(row, { images: e.target.checked })} />
                </td>
                <td>
                  <input
                    type="checkbox"
                    aria-label={`Stop while backing up ${row.stack}`}
                    checked={row.stopDuring}
                    disabled={!row.volumes}
                    title={row.volumes ? undefined : 'Tick Volumes first'}
                    onChange={(e) => void toggle(row, { stopDuring: e.target.checked })}
                  />
                </td>
              </tr>
              {open.has(row.stack) && (
                <tr className={styles.detail}>
                  <td colSpan={4}>
                    {row.mounts.length === 0 ? (
                      <span className={styles.muted}>No mounts.</span>
                    ) : (
                      <ul className={styles.mounts}>
                        {row.mounts.map((m) => (
                          <li key={`${m.kind}:${m.name}:${m.target}`}>
                            <span className={styles.muted}>{m.kind}</span>
                            <span className={styles.mono}>{m.name}</span>
                            <span className={styles.mono}>{m.target}</span>
                            <span>{m.bytes < 0 ? '—' : formatBytes(m.bytes)}</span>
                          </li>
                        ))}
                      </ul>
                    )}
                  </td>
                </tr>
              )}
            </Fragment>
          ))}
        </tbody>
      </table>
    </div>
  );
}
