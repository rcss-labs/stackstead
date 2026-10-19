import type { ReactNode } from 'react';
import styles from './PanelLabel.module.css';

export interface PanelLabelProps {
  children: ReactNode;
  tone?: 'muted' | 'faint' | 'danger' | 'warning' | 'success';
  as?: 'div' | 'span' | 'h2' | 'th';
  id?: string;
}

/** Mono uppercase section label: the identity's signature for headers and table heads. */
export function PanelLabel({ children, tone = 'muted', as: Tag = 'div', id }: PanelLabelProps) {
  return (
    <Tag className={styles.label} data-tone={tone} id={id}>
      {children}
    </Tag>
  );
}
