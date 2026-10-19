import { useState } from 'react';
import type { ReactNode } from 'react';
import styles from './Banner.module.css';

export interface BannerProps {
  kind: 'info' | 'warning' | 'danger';
  title: string;
  children?: ReactNode;
  dismissible?: boolean;
}

const icons = { info: 'ℹ', warning: '⚠', danger: '✖' } as const;

export function Banner({ kind, title, children, dismissible = false }: BannerProps) {
  const [dismissed, setDismissed] = useState(false);
  if (dismissed) return null;
  return (
    <div className={styles.banner} data-kind={kind} role={kind === 'danger' ? 'alert' : 'status'}>
      <span className={styles.icon} aria-hidden="true">
        {icons[kind]}
      </span>
      <div className={styles.body}>
        <strong>{title}</strong>
        {children && <div>{children}</div>}
      </div>
      {dismissible && (
        <button type="button" className={styles.close} onClick={() => setDismissed(true)}>
          Dismiss
        </button>
      )}
    </div>
  );
}
