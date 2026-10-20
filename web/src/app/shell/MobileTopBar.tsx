import { Link, useLocation } from 'react-router';
import { splitServerPath } from '$lib/router';
import { ICONS } from '$lib/shell/icons';
import { Led } from '../components/Led';
import { ServerSwitcher } from './ServerSwitcher';
import styles from './MobileTopBar.module.css';

export interface MobileTopBarProps {
  title: string;
  mono: boolean;
  attention: boolean;
  onOpenPalette: () => void;
  /** Follows a navigation; the app passes its unsaved-changes guard. */
  onNavigate?: (href: string) => void;
}

export function MobileTopBar({ title, mono, attention, onOpenPalette, onNavigate }: MobileTopBarProps) {
  // New stack belongs to the server being viewed, not always this one.
  const { slug } = splitServerPath(useLocation().pathname);
  const newStack = slug === null ? '/stacks/new' : `/s/${encodeURIComponent(slug)}/stacks/new`;
  return (
    <header className={styles.bar}>
      <Link to="/" className={styles.logo} aria-label="Stackstead">
        <img src="/containers.png" alt="" width="24" height="24" />
      </Link>
      <span className={styles.title} data-mono={mono ? 'true' : undefined}>
        {title}
      </span>
      <ServerSwitcher onNavigate={onNavigate} />
      {attention && <Led tone="danger" pulse size={7} label="Needs attention" />}
      <button type="button" className={styles.action} aria-label="Search or run a command" onClick={onOpenPalette}>
        <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d={ICONS.search} />
        </svg>
      </button>
      <Link to={newStack} className={`${styles.action} ${styles.primary}`} aria-label="New stack">
        <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d={ICONS.plus} />
        </svg>
      </Link>
    </header>
  );
}
