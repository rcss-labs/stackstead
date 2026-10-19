import { useEffect, useState } from 'react';
import { Link } from 'react-router';
import type { Operation } from '$lib/api/types';
import { formatDuration } from '$lib/format';
import { operationDurationMs, operationStateLook } from '$lib/stacks/operation';
import { Led } from '../components/Led';
import { RelativeTime } from '../components/RelativeTime';
import styles from './OperationsList.module.css';

export interface OperationsListProps {
  operations: Operation[];
  selected?: string;
  href: (id: string) => string;
}

export function OperationsList({ operations, selected, href }: OperationsListProps) {
  const [now, setNow] = useState(() => Date.now());
  const anyRunning = operations.some((o) => o.state === 'running');

  // One shared ticker, alive only while a running row is rendered.
  useEffect(() => {
    if (!anyRunning) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [anyRunning]);

  if (operations.length === 0) return <p className={styles.empty}>No operations yet.</p>;

  return (
    <ul className={styles.list}>
      {operations.map((o) => {
        const look = operationStateLook(o.state);
        const ms = operationDurationMs(o, now);
        const content = (
          <>
            <Led tone={look.tone} pulse={o.state === 'running'} size={7} label={look.label} />
            <span className={styles.action}>{o.action}</span>
            <span className={styles.detail}>{o.stack ? `docker compose ${o.action}` : 'host'}</span>
            <span className={styles.when}>
              <RelativeTime iso={o.startedAt} />
              <span className={styles.duration}>{ms === undefined ? '' : formatDuration(ms)}</span>
            </span>
          </>
        );
        return (
          <li key={o.id}>
            {o.stack ? (
              <Link to={href(o.id)} className={styles.row} aria-current={o.id === selected ? 'true' : undefined}>
                {content}
              </Link>
            ) : (
              <div className={styles.row}>{content}</div>
            )}
          </li>
        );
      })}
    </ul>
  );
}
