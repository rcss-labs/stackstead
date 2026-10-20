import { Fragment } from 'react';
import { Link } from 'react-router';
import type { StorageConsumer, StorageDeclaration } from '$lib/api/types';
import styles from './DetailScreen.module.css';

/** Containers using the item. Shared by the image and volume detail screens. */
export function ConsumersCard({ consumers }: { consumers: StorageConsumer[] }) {
  return (
    <section className={styles.card}>
      <h2 className={styles.cardTitle}>Containers</h2>
      <ul className={styles.list}>
        {consumers.map((consumer) => (
          <li key={consumer.containerId}>
            <Link className={styles.link} to={`/containers/${encodeURIComponent(consumer.name)}`}>
              {consumer.name}
            </Link>{' '}
            <span className={styles.muted}>{consumer.state}</span>
          </li>
        ))}
        {consumers.length === 0 && <li className={styles.muted}>No containers</li>}
      </ul>
    </section>
  );
}

/** Stack services that declare the item. */
export function DeclarationsCard({ declarations }: { declarations: StorageDeclaration[] }) {
  return (
    <section className={styles.card}>
      <h2 className={styles.cardTitle}>Declared by</h2>
      <ul className={styles.list}>
        {declarations.map((declaration) => (
          <li key={`${declaration.stack}/${declaration.service}/${declaration.ref}`}>
            <Link className={styles.link} to={`/stacks/${encodeURIComponent(declaration.stack)}`}>
              {declaration.stack}
            </Link>
            {' · '}
            {declaration.service} <span className={styles.muted}>({declaration.stackState})</span>
          </li>
        ))}
        {declarations.length === 0 && <li className={styles.muted}>No declarations</li>}
      </ul>
    </section>
  );
}

export function LabelsDetails({ labels }: { labels: Record<string, string> }) {
  const entries = Object.entries(labels);
  return (
    <details className={styles.details}>
      <summary>Labels</summary>
      <dl className={styles.dl}>
        {entries.map(([key, value]) => (
          <Fragment key={key}>
            <dt>{key}</dt>
            <dd>{value}</dd>
          </Fragment>
        ))}
        {entries.length === 0 && <dd className={styles.muted}>No labels</dd>}
      </dl>
    </details>
  );
}
