import type { ReactNode } from 'react';
import styles from './SplitPane.module.css';

export interface SplitPaneProps {
  rail: ReactNode;
  /** The mobile chip scroller; shown above the content under 760px. */
  chips: ReactNode;
  children: ReactNode;
  /** Pinned to the bottom of the right column (the log console, later). */
  console?: ReactNode;
}

export function SplitPane({ rail, chips, children, console }: SplitPaneProps) {
  return (
    <div className={styles.pane}>
      {rail}
      <div className={styles.main}>
        {chips}
        {children}
        {console && <div className={styles.console}>{console}</div>}
      </div>
    </div>
  );
}
