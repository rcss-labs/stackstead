import { useState } from 'react';
import type { ReactNode } from 'react';
import styles from './Banner.module.css';

export interface BannerProps {
  kind: 'info' | 'warning' | 'danger';
  title: string;
  children?: ReactNode;
  dismissible?: boolean;
}

// Stroke icons in a 24px box; text glyphs can render as emoji on some platforms.
const icons = {
  info: 'M12 11v6M12 7.5h.01M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z',
  warning: 'M12 4 2.5 20h19L12 4zM12 10v4.5M12 17.5h.01',
  danger: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zM9 9l6 6M15 9l-6 6',
} as const;

export function Banner({ kind, title, children, dismissible = false }: BannerProps) {
  const [dismissed, setDismissed] = useState(false);
  if (dismissed) return null;
  return (
    <div className={styles.banner} data-kind={kind} role={kind === 'danger' ? 'alert' : 'status'}>
      <svg className={styles.icon} width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d={icons[kind]} />
      </svg>
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
