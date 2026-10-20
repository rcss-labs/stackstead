import { Link } from 'react-router';
import { ICONS } from '$lib/shell/icons';
import { effectiveTheme, setTheme } from '$lib/stores/appearance';
import { live } from '$lib/stores/live';
import { useStore } from '../hooks/useStore';
import { PanelLabel } from '../components/PanelLabel';
import { HostBlock } from './HostBlock';
import { NavItem } from './NavItem';
import { NAV, type NavEntry } from './nav';
import styles from './Sidebar.module.css';

export interface SidebarProps {
  username?: string;
  onLogout: () => void;
  /** Path of the page being shown. */
  current: string;
  stackCount: number;
  attention: boolean;
  onOpenPalette: () => void;
}

const isMac = typeof navigator !== 'undefined' && /mac/i.test(navigator.platform);

export function Sidebar({ username, onLogout, current, stackCount, attention, onOpenPalette }: SidebarProps) {
  const dark = useStore(effectiveTheme) === 'dark';
  const status = useStore(live);
  const initials = (username ?? '').slice(0, 2).toUpperCase();
  const themeLabel = dark ? 'Switch to light theme' : 'Switch to dark theme';

  // Neutral "Docker" until the first status answer; only a real "unavailable" is called out.
  const dockerText =
    status.docker === 'unavailable' ? 'Docker unavailable' : status.docker === 'ok' && status.dockerVersion ? `Docker ${status.dockerVersion}` : 'Docker';
  const composeText = status.compose === 'unknown' ? null : status.compose === 'unavailable' ? 'compose unavailable' : `compose ${status.compose}`;
  const versions = composeText ? `${dockerText} · ${composeText}` : dockerText;

  const item = (entry: NavEntry) => (
    <NavItem
      key={entry.href}
      entry={entry}
      current={entry.match(current)}
      count={entry.href === '/' ? stackCount : undefined}
      alert={entry.href === '/' && attention}
    />
  );

  return (
    <nav aria-label="Main" className={styles.sidebar}>
      <Link to="/" className={styles.logo} aria-label="Stackstead">
        <img src="/containers.png" alt="" width="28" height="28" />
        <span className={styles.wordmark} aria-hidden="true">
          Stackstead
        </span>
      </Link>
      <HostBlock />
      <button type="button" className={styles.search} aria-label="Search or run a command" onClick={onOpenPalette}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d={ICONS.search} />
        </svg>
        <span className={styles.searchText} aria-hidden="true">
          Search or run…
        </span>
        <kbd className={styles.kbd} aria-hidden="true">
          {isMac ? '⌘K' : 'Ctrl K'}
        </kbd>
      </button>
      <div className={styles.group}>{NAV.filter((e) => e.group === 'main').map(item)}</div>
      <div className={styles.createLabel}>
        <PanelLabel tone="faint">Create</PanelLabel>
      </div>
      <div className={styles.group}>{NAV.filter((e) => e.group === 'create').map(item)}</div>
      <div className={styles.bottom}>
        <div className={styles.group}>{NAV.filter((e) => e.group === 'settings').map(item)}</div>
        <div className={styles.user}>
          <button
            type="button"
            className={styles.avatar}
            aria-label={username ? `Log out ${username}` : 'Log out'}
            title={`${username ?? ''} · Log out`}
            onClick={onLogout}
          >
            {initials}
          </button>
          <div className={styles.who}>
            <span className={styles.name}>{username}</span>
            <span className={styles.versions}>{versions}</span>
          </div>
          <button type="button" className={styles.theme} aria-label={themeLabel} title={themeLabel} onClick={() => setTheme(dark ? 'light' : 'dark')}>
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d={dark ? ICONS.sun : ICONS.moon} />
            </svg>
          </button>
        </div>
      </div>
    </nav>
  );
}
