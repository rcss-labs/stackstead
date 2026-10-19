import { Fragment } from 'react';
import { Link } from 'react-router';
import type { Operation } from '$lib/api/types';
import { breadcrumb } from '$lib/shell/breadcrumb';
import { live } from '$lib/stores/live';
import { useStore } from '../hooks/useStore';
import styles from './TopBar.module.css';

export interface TopBarProps {
  path: string;
  runningOps: Operation[];
}

export function TopBar({ path, runningOps }: TopBarProps) {
  const crumbs = breadcrumb(path);
  const { connection } = useStore(live);
  const liveState = connection === 'open' || connection === 'reconnecting' ? connection : undefined;
  const runningText = runningOps.length === 1 ? `${runningOps[0].action} · ${runningOps[0].stack}` : `${runningOps.length} running`;

  return (
    <header className={styles.topbar}>
      <nav aria-label="Breadcrumb" className={styles.crumbs}>
        {crumbs.map((crumb, i) => {
          const last = i === crumbs.length - 1;
          return (
            <Fragment key={i}>
              {i > 0 && (
                <span className={styles.sep} aria-hidden="true">
                  /
                </span>
              )}
              {crumb.href && !last ? (
                <Link to={crumb.href} className={styles.link}>
                  {crumb.label}
                </Link>
              ) : (
                <span className={styles.current} data-mono={crumb.mono ? 'true' : undefined} aria-current={last ? 'page' : undefined}>
                  {crumb.label}
                </span>
              )}
            </Fragment>
          );
        })}
      </nav>
      <span className={styles.spacer} />
      {runningOps.length > 0 && (
        <span className={`${styles.pill} ${styles.running}`} role="status">
          <span className={styles.ring} aria-hidden="true" />
          {runningText}
        </span>
      )}
      {liveState && (
        <span className={styles.pill} data-live={liveState}>
          <span className={styles.dot} aria-hidden="true" />
          {liveState === 'open' ? 'live' : 'reconnecting'}
        </span>
      )}
    </header>
  );
}
