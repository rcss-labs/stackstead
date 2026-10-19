import { useState } from 'react';
import { Link } from 'react-router';
import type { TopEntry } from '$lib/api/types';
import { formatBytes, formatPercent } from '$lib/format';
import { Segmented } from '../components/Segmented';
import styles from './SideColumn.module.css';

type Metric = 'cpu' | 'mem';

const OPTIONS = [
  { value: 'cpu', label: 'CPU' },
  { value: 'mem', label: 'Memory' },
];

export interface TopConsumersProps {
  /** Undefined while not loaded or when the fetch failed. */
  top: { cpu: TopEntry[]; mem: TopEntry[] } | undefined;
  /** Compose project to stack name; they differ for imported stacks. */
  stackNames: Record<string, string>;
}

export function TopConsumers({ top, stackNames }: TopConsumersProps) {
  const [metric, setMetric] = useState<Metric>('cpu');
  const rows = (top?.[metric] ?? []).slice(0, 5);
  const max = Math.max(0, ...rows.map((r) => r.value));
  const ratio = (v: number) => (max > 0 ? Math.round((v / max) * 100) : 0);
  const label = (r: TopEntry) => {
    const owner = stackNames[r.project] ?? r.project;
    return owner ? `${r.container} · ${owner}` : r.container;
  };

  return (
    <section className={styles.card}>
      <div className={styles.titleRow}>
        <h2 className={styles.titleText}>{metric === 'cpu' ? 'Top CPU' : 'Top memory'}</h2>
        <Segmented label="Metric" size="sm" options={OPTIONS} value={metric} onChange={(v) => setMetric(v as Metric)} />
      </div>
      {top && rows.length === 0 && <p className={styles.empty}>No samples yet.</p>}
      {rows.length > 0 && (
        <ul className={styles.consumers}>
          {rows.map((r) => (
            <li key={`${r.project}|${r.container}`}>
              <div className={styles.line}>
                <Link className={styles.name} to={`/containers/${encodeURIComponent(r.container)}`} title={label(r)}>
                  {label(r)}
                </Link>
                <span className={styles.value}>{metric === 'cpu' ? formatPercent(r.value) : formatBytes(r.value)}</span>
              </div>
              <div className={styles.bar} data-bar={metric} aria-hidden="true">
                <span style={{ width: `${ratio(r.value)}%` }} />
              </div>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
