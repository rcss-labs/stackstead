import type { ReactNode } from 'react';
import styles from './Chip.module.css';

export interface ChipProps {
  pressed: boolean;
  onClick: () => void;
  count?: number | string;
  tone?: 'danger';
  children: ReactNode;
}

/** Toggle filter chip. */
export function Chip({ pressed, onClick, count, tone, children }: ChipProps) {
  return (
    <button type="button" className={styles.chip} aria-pressed={pressed} data-tone={tone} onClick={onClick}>
      {children}
      {count !== undefined && <span className={styles.count}>{count}</span>}
    </button>
  );
}
