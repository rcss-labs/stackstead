import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import { ICONS } from '$lib/shell/icons';
import { splitServerPath } from '$lib/router';
import type { PeerStatus, PeerView } from '$lib/api/types';
import { Led, type LedProps } from '../components/Led';
import { CONNECT_SERVER_HREF } from './navItems';
import styles from './ServerMenu.module.css';

export interface ServerMenuProps {
  /** Path of the page being shown; the server in it is the one the button names. */
  current: string;
  localName: string;
  /** Shows the muted "this server" tag; off while the name is still the generic fallback. */
  localTagged: boolean;
  localStackCount: number;
  peers: PeerView[];
  /** Follows a choice; the app passes its unsaved-changes guard. Defaults to the router. */
  onNavigate?: (href: string) => void;
}

const STATUS: Record<PeerStatus, { text: string; tone: LedProps['tone'] }> = {
  reachable: { text: 'reachable', tone: 'success' },
  unreachable: { text: 'unreachable', tone: 'danger' },
  wrong_instance: { text: 'wrong instance', tone: 'danger' },
  incompatible: { text: 'incompatible', tone: 'warning' },
  removed: { text: 'removed', tone: 'danger' },
  pending: { text: 'connecting', tone: 'muted' },
};

const icon = (name: keyof typeof ICONS, size = 14) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d={ICONS[name]} />
  </svg>
);

/** The server switcher: this server, connected servers, All servers and Connect a server, in one disclosure panel (plain buttons, no menu roles). */
export function ServerMenu({ current, localName, localTagged, localStackCount, peers, onNavigate }: ServerMenuProps) {
  const navigate = useNavigate();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const viewed = splitServerPath(current).slug;
  const peer = peers.find((p) => p.slug === viewed);
  const shownName = viewed === null ? localName : (peer?.name ?? viewed);
  const total = peers.reduce((sum, p) => sum + (p.status === 'reachable' ? p.stackCount : 0), localStackCount);

  useEffect(() => {
    if (!open) return;
    const away = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener('pointerdown', away);
    return () => document.removeEventListener('pointerdown', away);
  }, [open]);

  const choose = (href: string) => {
    setOpen(false);
    (onNavigate ?? navigate)(href);
  };

  const onKeyDown = (event: React.KeyboardEvent) => {
    if (event.key === 'Escape' && open) {
      event.stopPropagation();
      setOpen(false);
      button.current?.focus();
    }
  };

  const item = (key: string, href: string, selected: boolean, body: React.ReactNode) => (
    <button key={key} type="button" className={styles.item} aria-current={selected ? 'true' : undefined} onClick={() => choose(href)}>
      {body}
    </button>
  );

  return (
    <div ref={root} className={styles.root} onKeyDown={onKeyDown}>
      <button
        ref={button}
        type="button"
        className={styles.button}
        aria-expanded={open}
        aria-label={`Switch server, ${shownName}`}
        onClick={() => setOpen(!open)}
      >
        <img src="/containers.png" alt="" width="26" height="26" className={styles.mark} />
        <span className={styles.names}>
          <span className={styles.name}>{shownName}</span>
          <span className={styles.kind}>{viewed === null ? 'this server' : 'connected server'}</span>
        </span>
        {icon('down')}
      </button>
      {open && (
        <div role="group" aria-label="Servers" className={styles.menu}>
          {item(
            'local',
            '/',
            viewed === null && current !== '/all',
            <>
              <Led tone="success" size={7} label="reachable" />
              <span className={styles.itemName}>{localName}</span>
              {localTagged && <span className={styles.tag}>this server</span>}
              <span className={styles.summary}>{localStackCount}</span>
            </>,
          )}
          {peers.map((p) => {
            const status = STATUS[p.status];
            return item(
              p.id,
              `/s/${encodeURIComponent(p.slug)}`,
              p.slug === viewed,
              <>
                <Led tone={status.tone} size={7} label={status.text} />
                <span className={styles.itemName}>{p.name}</span>
                <span className={styles.summary}>{p.status === 'reachable' ? p.stackCount : status.text}</span>
              </>,
            );
          })}
          {peers.length > 0 &&
            item(
              'all',
              '/all',
              current === '/all',
              <>
                {icon('grid', 16)}
                <span className={styles.itemName}>All servers</span>
                <span className={styles.summary}>{total}</span>
              </>,
            )}
          {item(
            'connect',
            CONNECT_SERVER_HREF,
            false,
            <>
              {icon('plus', 16)}
              <span className={styles.itemName}>Connect a server</span>
            </>,
          )}
        </div>
      )}
    </div>
  );
}
