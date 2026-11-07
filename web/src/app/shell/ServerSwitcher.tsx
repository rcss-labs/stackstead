import { useLocation, useNavigate } from 'react-router';
import { splitServerPath } from '$lib/router';
import { connectedPeers, peers, selfName } from '$lib/servers/peers';
import { useStore } from '../hooks/useStore';
import { CONNECT_SERVER_HREF } from './nav';
import styles from './MobileTopBar.module.css';

export interface ServerSwitcherProps {
  onNavigate?: (href: string) => void;
}

const ALL = '__all';
const CONNECT = '__connect';

/** Mobile server picker; absent until a server is connected. Also reaches All servers and Connect a server. */
export function ServerSwitcher({ onNavigate }: ServerSwitcherProps) {
  const ownName = useStore(selfName);
  const items = connectedPeers(useStore(peers).items);
  const { pathname } = useLocation();
  const navigate = useNavigate();
  if (items.length === 0) return null;
  const { slug } = splitServerPath(pathname);
  return (
    <select
      className={styles.switcher}
      aria-label="Server"
      value={pathname === '/all' ? ALL : (slug ?? '')}
      onChange={(event) => {
        const next = event.target.value;
        const href = next === '' ? '/' : next === ALL ? '/all' : next === CONNECT ? CONNECT_SERVER_HREF : `/s/${encodeURIComponent(next)}`;
        (onNavigate ?? navigate)(href);
      }}
    >
      <option value="">{ownName ? `${ownName} (this server)` : 'this server'}</option>
      {items.map((peer) => (
        <option key={peer.id} value={peer.slug}>
          {peer.name}
        </option>
      ))}
      <option value={ALL}>All servers</option>
      <option value={CONNECT}>Connect a server</option>
    </select>
  );
}
