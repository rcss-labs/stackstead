import type { ReactNode } from 'react';
import styles from './EmptyState.module.css';

export interface EmptyStateProps {
  title: string;
  description?: string;
  /** h1 when the empty state is the page's only heading. */
  as?: 'h1' | 'h2';
  children?: ReactNode;
}

export function EmptyState({ title, description, as: Heading = 'h2', children }: EmptyStateProps) {
  return (
    <div className={styles.empty}>
      <Heading>{title}</Heading>
      {description && <p>{description}</p>}
      {children}
    </div>
  );
}
