import { Link } from 'react-router';
import { ICONS } from '$lib/shell/icons';
import { Led } from '../components/Led';
import type { NavEntry } from './nav';
import styles from './NavItem.module.css';

export interface NavItemProps {
  entry: NavEntry;
  current: boolean;
  count?: number | string;
  alert?: boolean;
}

export function NavItem({ entry, current, count, alert = false }: NavItemProps) {
  return (
    <Link
      to={entry.href}
      className={styles.item}
      aria-label={entry.label}
      title={entry.label}
      aria-current={current ? 'page' : undefined}
      data-current={current ? 'true' : undefined}
    >
      <span className={styles.bar} aria-hidden="true" />
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
        <path d={ICONS[entry.icon]} />
      </svg>
      <span className={styles.label} aria-hidden="true">
        {entry.label}
      </span>
      {alert && <Led tone="danger" pulse size={7} />}
      {count !== undefined && (
        <span className={styles.count} aria-hidden="true">
          {count}
        </span>
      )}
    </Link>
  );
}
