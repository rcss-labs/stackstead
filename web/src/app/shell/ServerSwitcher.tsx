import { useLocation, useNavigate } from 'react-router';
import { splitServerPath } from '$lib/router';
import { connectedPeers, peers } from '$lib/servers/peers';
import { useStore } from '../hooks/useStore';
import styles from './MobileTopBar.module.css';

export interface ServerSwitcherProps {
  onNavigate?: (href: string) => void;
}

/** Mobile server picker; absent until a server is connected. */
export function ServerSwitcher({ onNavigate }: ServerSwitcherProps) {
  const items = connectedPeers(useStore(peers).items);
  const { pathname } = useLocation();
  const navigate = useNavigate();
  if (items.length === 0) return null;
  const { slug } = splitServerPath(pathname);
  return (
    <select
      className={styles.switcher}
      aria-label="Server"
      value={slug ?? ''}
      onChange={(event) => {
        const next = event.target.value;
        (onNavigate ?? navigate)(next === '' ? '/' : `/s/${encodeURIComponent(next)}`);
      }}
    >
      <option value="">this server</option>
      {items.map((peer) => (
        <option key={peer.id} value={peer.slug}>
          {peer.name}
        </option>
      ))}
    </select>
  );
}
