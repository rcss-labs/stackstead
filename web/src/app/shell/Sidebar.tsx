import { ICONS } from '$lib/shell/icons';
import { splitServerPath } from '$lib/router';
import { effectiveTheme, setTheme } from '$lib/stores/appearance';
import { connectedPeers, peers, selfName } from '$lib/servers/peers';
import { live } from '$lib/stores/live';
import { useStore } from '../hooks/useStore';
import { localScope } from '../servers/ServerContext';
import { PanelLabel } from '../components/PanelLabel';
import { HostBlock } from './HostBlock';
import { NavItem } from './NavItem';
import { ServerMenu } from './ServerMenu';
import { SETTINGS, serverNav, type NavEntry } from './navItems';
import styles from './Sidebar.module.css';

export interface SidebarProps {
  username?: string;
  onLogout: () => void;
  /** Path of the page being shown. */
  current: string;
  stackCount: number;
  attention: boolean;
  onOpenPalette: () => void;
  /** Follows a server switcher choice; the app passes its unsaved-changes guard. */
  onNavigate?: (href: string) => void;
}

const isMac = typeof navigator !== 'undefined' && /mac/i.test(navigator.platform);

export function Sidebar({ username, onLogout, current, stackCount, attention, onOpenPalette, onNavigate }: SidebarProps) {
  const dark = useStore(effectiveTheme) === 'dark';
  const status = useStore(live);
  const peerList = connectedPeers(useStore(peers).items);
  const ownName = useStore(selfName);
  const initials = (username ?? '').slice(0, 2).toUpperCase();
  const themeLabel = dark ? 'Switch to light theme' : 'Switch to dark theme';
  // The pages follow the server being viewed; Settings, like the account, belongs to this server.
  const viewed = splitServerPath(current).slug;
  const entries = serverNav(viewed);
  const viewedStackCount = viewed === null ? stackCount : (peerList.find((p) => p.slug === viewed)?.stackCount ?? 0);

  // Neutral "Docker" until the first status answer; only a real "unavailable" is called out.
  const dockerText =
    status.docker === 'unavailable' ? 'Docker unavailable' : status.docker === 'ok' && status.dockerVersion ? `Docker ${status.dockerVersion}` : 'Docker';
  const composeText = status.compose === 'unknown' ? null : status.compose === 'unavailable' ? 'compose unavailable' : `compose ${status.compose}`;
  const versions = composeText ? `${dockerText} · ${composeText}` : dockerText;

  const item = (entry: NavEntry) => {
    const overview = entry.label === 'Overview';
    return (
      <NavItem
        key={entry.href}
        entry={entry}
        current={entry.match(current)}
        count={overview ? viewedStackCount : undefined}
        alert={overview && viewed === null && attention}
      />
    );
  };
  const group = (name: NavEntry['group']) => entries.filter((e) => e.group === name).map(item);

  return (
    <nav aria-label="Main" className={styles.sidebar}>
      <ServerMenu
        current={current}
        localName={ownName ?? localScope.name}
        localTagged={ownName !== undefined}
        localStackCount={stackCount}
        peers={peerList}
        onNavigate={onNavigate}
      />
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
      <div className={styles.scroll}>
        <div className={styles.group}>{group('main')}</div>
        <div className={styles.groupLabel}>
          <PanelLabel tone="faint">Resources</PanelLabel>
        </div>
        <div className={styles.group}>{group('resources')}</div>
        <div className={styles.groupLabel}>
          <PanelLabel tone="faint">Add</PanelLabel>
        </div>
        <div className={styles.group}>{group('create')}</div>
        <div className={styles.vitals}>
          <HostBlock />
        </div>
      </div>
      <div className={styles.bottom}>
        <div className={styles.group}>{item(SETTINGS)}</div>
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
