import type { CSSProperties, ReactNode } from 'react';
import type { StorageKindTotals } from '$lib/api/types';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
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
  /** Filter chips and search, between the header and the rows. */
  toolbar?: ReactNode;
  /** Shown after the rows inside the card, e.g. a pager. */
  footer?: ReactNode;
  /** A failed read: replaces the rows with the error and a Retry. */
  failure?: { message: string; onRetry: () => void };
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

export function StorageTable({ label, columns, status, titleId, sizes, children, empty, toolbar, footer, failure }: StorageTableProps) {
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
      {failure ? (
        <div className={styles.failure}>
          <Banner kind="danger" title="Could not load storage">
            {failure.message}
          </Banner>
          <Button variant="secondary" onClick={failure.onRetry}>
            Retry
          </Button>
        </div>
      ) : (
        <>
          {toolbar}
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
        </>
      )}
      {!failure && empty && <p className={styles.none}>{empty}</p>}
      {!failure && footer}
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

/** `primary` and `end` keep the first line on narrow screens, `action` sits below them, `sub` drops below as a small line and `desktop` is hidden. */
export function StorageCell({ children, mono = false, place = 'primary', title }: { children: ReactNode; mono?: boolean; place?: 'primary' | 'end' | 'sub' | 'desktop' | 'action'; title?: string }) {
  return (
    <div role="cell" className={styles.cell} data-mono={mono ? 'true' : undefined} data-place={place} title={title}>
      {children}
    </div>
  );
}
