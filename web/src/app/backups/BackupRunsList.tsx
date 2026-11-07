import { Fragment, useState } from 'react';
import type { BackupRun } from '$lib/api/backupTypes';
import { formatBytes, formatDuration } from '$lib/format';
import { Banner } from '../components/Banner';
import { EmptyState } from '../components/EmptyState';
import { RelativeTime } from '../components/RelativeTime';
import { Spinner } from '../components/Spinner';
import { StatusBadge } from '../components/StatusBadge';
import type { RunsState } from './status';
import styles from './backups.module.css';

// The shared badge knows "succeeded"; backup runs say "success".
const badgeStatus = (status: BackupRun['status']) => (status === 'success' ? 'succeeded' : status);

function duration(run: BackupRun): string {
  if (!run.endedAt) return '—';
  return formatDuration(new Date(run.endedAt).getTime() - new Date(run.startedAt).getTime());
}

/** Recent runs, newest first, each expandable to its per-item results. */
export function BackupRunsList({ state }: { state: RunsState }) {
  const [open, setOpen] = useState<Set<number>>(new Set());

  function toggleOpen(id: number) {
    setOpen((current) => {
      const next = new Set(current);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  }

  if (state.kind === 'error')
    return (
      <Banner kind="danger" title="Runs">
        {state.message}
      </Banner>
    );
  if (state.kind === 'loading') return <Spinner />;
  const runs = state.runs;
  if (runs.length === 0) return <EmptyState title="No backups yet" description="Runs appear here once a backup has started." />;

  return (
    <div className={styles.scroll}>
      <table className={styles.table}>
        <thead>
          <tr>
            <th scope="col">Started</th>
            <th scope="col">Duration</th>
            <th scope="col">Size</th>
            <th scope="col">Status</th>
            <th scope="col">Destinations</th>
          </tr>
        </thead>
        <tbody>
          {runs.map((run) => (
            <Fragment key={run.id}>
              <tr>
                <td>
                  <button
                    type="button"
                    className={styles.expand}
                    aria-expanded={open.has(run.id)}
                    aria-label={`Show items of run ${run.id}`}
                    onClick={() => toggleOpen(run.id)}
                  >
                    {open.has(run.id) ? '▾' : '▸'} <RelativeTime iso={run.startedAt} />
                  </button>
                </td>
                <td>{duration(run)}</td>
                <td>{run.size > 0 ? formatBytes(run.size) : '—'}</td>
                <td>
                  <StatusBadge status={badgeStatus(run.status)} />
                </td>
                <td>{run.destinations.length ? run.destinations.join(', ') : '—'}</td>
              </tr>
              {open.has(run.id) && (
                <tr className={styles.detail}>
                  <td colSpan={5}>
                    {run.error && <p className={styles.result}>{run.error}</p>}
                    {run.items.length === 0 ? (
                      <span className={styles.muted}>No items.</span>
                    ) : (
                      <ul className={styles.mounts}>
                        {run.items.map((item, i) => (
                          <li key={i}>
                            <span className={styles.muted}>{item.kind}</span>
                            <span className={styles.mono}>{[item.stack, item.name].filter(Boolean).join(' / ') || '—'}</span>
                            <span>{item.status}</span>
                            {item.bytes ? <span>{formatBytes(item.bytes)}</span> : null}
                            {item.note && <span className={styles.muted}>{item.note}</span>}
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
