import { Link } from 'react-router';
import { splitServerPath } from '$lib/router';
import { ICONS } from '$lib/shell/icons';
import { Led } from '../components/Led';
import { tabBarNav } from './nav';
import styles from './TabBar.module.css';

export interface TabBarProps {
  current: string;
  attention: boolean;
}

export function TabBar({ current, attention: localAttention }: TabBarProps) {
  const { slug } = splitServerPath(current);
  // The attention light describes this server's stacks, so it stays off while a connected server is open.
  const attention = localAttention && slug === null;
  return (
    <nav aria-label="Main" className={styles.bar}>
      {tabBarNav(slug).map((entry) => {
        const active = entry.match(current);
        return (
          <Link key={entry.href} to={entry.href} className={styles.tab} aria-label={attention && entry.label === 'Stacks' ? `${entry.label}, needs attention` : entry.label} aria-current={active ? 'page' : undefined}>
            <span className={styles.icon}>
              <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d={ICONS[entry.icon]} />
              </svg>
              {attention && entry.label === 'Stacks' && (
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
