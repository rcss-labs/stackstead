import styles from './Led.module.css';

export interface LedProps {
  tone: 'success' | 'warning' | 'danger' | 'info' | 'muted' | 'faint';
  pulse?: boolean;
  size?: 7 | 8;
  label?: string;
}

/** Square status light. Decorative unless a label is given. */
export function Led({ tone, pulse = false, size = 8, label }: LedProps) {
  return (
    <span
      className={styles.led}
      data-tone={tone}
      data-pulse={pulse ? 'true' : undefined}
      data-size={size}
      {...(label ? { role: 'img', 'aria-label': label } : { 'aria-hidden': true })}
    />
  );
}
