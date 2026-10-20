import type { ReactNode } from 'react';
import { Link } from 'react-router';
import type { StorageConsumer, StorageDeclaration } from '$lib/api/types';
import styles from './StorageTable.module.css';

/** Containers using a row and the stacks declaring it; `children` carries the protection note. */
export function UsedBy({ consumers, declarations, children }: { consumers: StorageConsumer[]; declarations: StorageDeclaration[]; children?: ReactNode }) {
  const empty = consumers.length === 0 && declarations.length === 0;
  return (
    <div className={styles.usedBy}>
      {consumers.map((consumer) => (
        <div key={consumer.containerId || consumer.name} className={consumer.state === 'running' ? undefined : styles.muted}>
          <Link className={styles.mono} to={`/containers/${encodeURIComponent(consumer.name)}`}>
            {consumer.name}
          </Link>{' '}
          <span className={styles.muted}>({consumer.state})</span>
        </div>
      ))}
      {declarations.map((declaration) => (
        <div key={`${declaration.stack}/${declaration.service}/${declaration.ref}`}>
          <Link to={`/stacks/${encodeURIComponent(declaration.stack)}`}>{declaration.stack}</Link> <span className={styles.muted}>({declaration.stackState})</span>
        </div>
      ))}
      {empty && '—'}
      {children}
    </div>
  );
}
