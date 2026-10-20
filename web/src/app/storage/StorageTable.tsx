import type { CSSProperties, ReactNode } from 'react';
import type { StorageKindTotals } from '$lib/api/types';
import { Banner } from '../components/Banner';
import { PanelLabel } from '../components/PanelLabel';
import { RelativeTime } from '../components/RelativeTime';
import styles from './StorageTable.module.css';

export interface StorageColumn {
  key: string;
  label: string;
  /** A grid track such as `100px`; defaults to `minmax(0, 1fr)`. */
  width?: string;
}

export interface StorageTableProps {
  label: string;
  columns: StorageColumn[];
  /** Visible summary such as `3 images · sorted by size`; the screen announces it through its own persistent status region. */
  status: string;
  /** Id for the title, so a surrounding section can label itself with it instead of repeating the heading. */
  titleId?: string;
  sizes?: StorageKindTotals;
  children: ReactNode;
  empty?: string;
}

function SizesAge({ sizes }: { sizes: StorageKindTotals }) {
  if (sizes.sizesAt) {
    return (
      <>
        Sizes from <RelativeTime iso={sizes.sizesAt} />
        {sizes.refreshing && ' · refreshing…'}
      </>
    );
  }
  return <>{sizes.refreshing ? 'Sizes calculating…' : 'Sizes not available yet'}</>;
}

export function StorageTable({ label, columns, status, titleId, sizes, children, empty }: StorageTableProps) {
  const cols = columns.map((c) => c.width ?? 'minmax(0, 1fr)').join(' ');
  return (
    <div className={styles.card}>
      <div className={styles.header}>
        <h2 id={titleId} className={styles.title}>
          {label}
        </h2>
        <p className={styles.status}>{status}</p>
        <span className={styles.spacer} />
        {sizes && (
          <span className={styles.age}>
            <SizesAge sizes={sizes} />
          </span>
        )}
      </div>
      {sizes?.error && (
        <div className={styles.banner}>
          <Banner kind="warning" title="Sizes unavailable">
            {sizes.error}
          </Banner>
        </div>
      )}
      <div role="table" aria-label={label} className={styles.table} style={{ '--cols': cols } as CSSProperties}>
        <div role="row" className={`${styles.row} ${styles.columns}`}>
          {columns.map((c) => (
            <div key={c.key} role="columnheader">
              <PanelLabel as="div">{c.label}</PanelLabel>
            </div>
          ))}
        </div>
        <div role="rowgroup">{children}</div>
      </div>
      {empty && <p className={styles.none}>{empty}</p>}
    </div>
  );
}

export function StorageRow({ children }: { children: ReactNode }) {
  return (
    <div role="row" className={styles.row}>
      {children}
    </div>
  );
}

/** `primary` and `end` keep the first line on narrow screens; every other cell drops below as a small line. */
export function StorageCell({ children, mono = false, place = 'primary', title }: { children: ReactNode; mono?: boolean; place?: 'primary' | 'end' | 'sub'; title?: string }) {
  return (
    <div role="cell" className={styles.cell} data-mono={mono ? 'true' : undefined} data-place={place} title={title}>
      {children}
    </div>
  );
}
