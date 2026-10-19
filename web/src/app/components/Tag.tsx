import type { ReactNode } from 'react';
import styles from './Tag.module.css';

export interface TagProps {
  tone?: 'muted' | 'info' | 'warning' | 'danger' | 'success';
  mono?: boolean;
  children: ReactNode;
}

export function Tag({ tone = 'muted', mono = true, children }: TagProps) {
  return (
    <span className={styles.tag} data-tone={tone} data-mono={mono ? 'true' : undefined}>
      {children}
    </span>
  );
}
