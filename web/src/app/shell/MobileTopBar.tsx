import { Link } from 'react-router';
import { ICONS } from '$lib/shell/icons';
import { Led } from '../components/Led';
import styles from './MobileTopBar.module.css';

export interface MobileTopBarProps {
  title: string;
  mono: boolean;
  attention: boolean;
  onOpenPalette: () => void;
}

export function MobileTopBar({ title, mono, attention, onOpenPalette }: MobileTopBarProps) {
  return (
    <header className={styles.bar}>
      <Link to="/" className={styles.logo} aria-label="Stackstead">
        <img src="/containers.png" alt="" width="24" height="24" />
      </Link>
      <h1 className={styles.title} data-mono={mono ? 'true' : undefined}>
        {title}
      </h1>
      {attention && <Led tone="danger" pulse size={7} />}
      <button type="button" className={styles.action} aria-label="Search or run a command" onClick={onOpenPalette}>
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d={ICONS.search} />
        </svg>
      </button>
      <Link to="/stacks/new" className={styles.action} aria-label="New stack">
        <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d={ICONS.plus} />
        </svg>
      </Link>
    </header>
  );
}
