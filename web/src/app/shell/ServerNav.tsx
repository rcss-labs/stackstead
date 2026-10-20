import { useNavigate } from 'react-router';
import { splitServerPath } from '$lib/router';
import type { PeerStatus, PeerView } from '$lib/api/types';
import { Led, type LedProps } from '../components/Led';
import { NavItem } from './NavItem';
import { serverNav, type NavEntry } from './nav';
import styles from './ServerNav.module.css';

export interface ServerNavProps {
  /** Path of the page being shown. */
  current: string;
  localName: string;
  localStackCount: number;
  peers: PeerView[];
  /** This server has a stack that needs attention. */
  attention: boolean;
  /** Follows a heading click; the app passes its unsaved-changes guard. Defaults to the router. */
  onNavigate?: (href: string) => void;
}

const ALL_STACKS: NavEntry = { href: '/all', label: 'All stacks', icon: 'stacks', match: (p) => p === '/all', group: 'main' };

const STATUS: Record<PeerStatus, { text: string; tone: LedProps['tone'] }> = {
  reachable: { text: 'reachable', tone: 'success' },
  unreachable: { text: 'unreachable', tone: 'danger' },
  wrong_instance: { text: 'wrong instance', tone: 'danger' },
  incompatible: { text: 'incompatible', tone: 'warning' },
  removed: { text: 'removed', tone: 'danger' },
  pending: { text: 'connecting', tone: 'muted' },
};

interface ServerEntry {
  key: string;
  slug: string | null;
  name: string;
  status: PeerStatus;
  stackCount: number;
}

/** The accordion: All stacks, then one section per server; only the server being viewed lists its pages. */
export function ServerNav({ current, localName, localStackCount, peers, attention, onNavigate }: ServerNavProps) {
  const navigate = useNavigate();
  const viewed = splitServerPath(current).slug;
  const servers: ServerEntry[] = [
    { key: 'local', slug: null, name: localName, status: 'reachable', stackCount: localStackCount },
    ...peers.map((p) => ({ key: p.id, slug: p.slug, name: p.name, status: p.status, stackCount: p.stackCount })),
  ];
  const total = servers.reduce((sum, s) => sum + (s.status === 'reachable' ? s.stackCount : 0), 0);

  return (
    <>
      <div className={styles.group}>
        <NavItem entry={ALL_STACKS} current={ALL_STACKS.match(current)} count={total} />
      </div>
      {servers.map((server) => {
        const open = server.slug === viewed;
        const entries = serverNav(server.slug);
        const status = STATUS[server.status];
        const stacksHref = entries[0].href;
        return (
          <section key={server.key} className={styles.server}>
            <button type="button" className={styles.heading} aria-expanded={open} onClick={() => (onNavigate ?? navigate)(stacksHref)}>
              <Led tone={status.tone} size={7} label={status.text} />
              <span className={styles.name}>{server.name}</span>
              {!open && <span className={styles.summary}>{server.status === 'reachable' ? server.stackCount : status.text}</span>}
            </button>
            {open && (
              <div className={styles.entries}>
                {entries.map((entry, i) => (
                  <NavItem
                    key={entry.href}
                    entry={entry}
                    current={entry.match(current)}
                    count={i === 0 ? server.stackCount : undefined}
                    alert={i === 0 && server.slug === null && attention}
                  />
                ))}
              </div>
            )}
          </section>
        );
      })}
    </>
  );
}
