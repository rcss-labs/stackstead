import type { ReactNode } from 'react';
import styles from './SettingsCard.module.css';

export interface SettingsCardProps {
  id: string;
  title: string;
  headerEnd?: ReactNode;
  footer?: ReactNode;
  children: ReactNode;
  className?: string;
}

export function SettingsCard({ id, title, headerEnd, footer, children, className }: SettingsCardProps) {
  return (
    <section className={[styles.card, className].filter(Boolean).join(' ')} aria-labelledby={id}>
      <header className={styles.header}>
        <h2 id={id} className={styles.title}>
          {title}
        </h2>
        {headerEnd && (
          <>
            <span className={styles.spacer} />
            {headerEnd}
          </>
        )}
      </header>
      <div className={styles.body}>{children}</div>
      {footer && <footer className={styles.footer}>{footer}</footer>}
    </section>
  );
}

export interface PillProps {
  tone: 'success' | 'accent' | 'danger' | 'muted' | 'info';
  children: ReactNode;
}

export function Pill({ tone, children }: PillProps) {
  return (
    <span className={styles.pill} data-tone={tone}>
      {children}
    </span>
  );
}
