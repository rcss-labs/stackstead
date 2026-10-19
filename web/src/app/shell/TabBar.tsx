import { Link } from 'react-router';
import { ICONS } from '$lib/shell/icons';
import { Led } from '../components/Led';
import { TAB_BAR } from './nav';
import styles from './TabBar.module.css';

export interface TabBarProps {
  current: string;
  attention: boolean;
}

export function TabBar({ current, attention }: TabBarProps) {
  return (
    <nav aria-label="Main" className={styles.bar}>
      {TAB_BAR.map((entry) => {
        const active = entry.match(current);
        return (
          <Link key={entry.href} to={entry.href} className={styles.tab} aria-label={entry.label} aria-current={active ? 'page' : undefined}>
            <span className={styles.icon}>
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d={ICONS[entry.icon]} />
              </svg>
              {attention && entry.href === '/' && (
                <span className={styles.alert}>
                  <Led tone="danger" pulse size={7} />
                </span>
              )}
            </span>
            <span className={styles.label} aria-hidden="true">
              {entry.label}
            </span>
          </Link>
        );
      })}
    </nav>
  );
}
