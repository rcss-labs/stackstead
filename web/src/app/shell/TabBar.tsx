import { Link } from 'react-router';
import { splitServerPath } from '$lib/router';
import { ICONS } from '$lib/shell/icons';
import { Led } from '../components/Led';
import { activeTab, tabBarNav } from './navItems';
import styles from './TabBar.module.css';

export interface TabBarProps {
  current: string;
  attention: boolean;
  /** The Search tab opens the palette; it is a button, not a page. */
  onOpenPalette: () => void;
}

export function TabBar({ current, attention: localAttention, onOpenPalette }: TabBarProps) {
  const { slug } = splitServerPath(current);
  // The attention light describes this server's stacks, so it stays off while a connected server is open.
  const attention = localAttention && slug === null;
  const active = activeTab(current);
  return (
    <nav aria-label="Main" className={styles.bar}>
      {tabBarNav(slug).map((tab) => {
        const alert = attention && tab.key === 'home';
        const label = alert ? `${tab.label}, needs attention` : tab.label;
        const body = (
          <>
            <span className={styles.icon}>
              <svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d={ICONS[tab.icon]} />
              </svg>
              {alert && (
                <span className={styles.alert}>
                  <Led tone="danger" pulse size={7} />
                </span>
              )}
            </span>
            <span className={styles.label} aria-hidden="true">
              {tab.label}
            </span>
          </>
        );
        if (tab.href === undefined) {
          return (
            <button key={tab.key} type="button" className={styles.tab} aria-label={label} onClick={onOpenPalette}>
              {body}
            </button>
          );
        }
        return (
          <Link key={tab.key} to={tab.href} className={styles.tab} aria-label={label} aria-current={active === tab.key ? 'page' : undefined}>
            {body}
          </Link>
        );
      })}
    </nav>
  );
}
