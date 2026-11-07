import { Link } from 'react-router';
import type { FleetServer, HostSnapshot } from '$lib/api/types';
import { Led } from '../components/Led';
import { RelativeTime } from '../components/RelativeTime';
import styles from './ServerTiles.module.css';

export interface ServerTilesProps {
  servers: FleetServer[];
  /** Stacks counted from rows that were kept; an unreachable server is never counted. */
  stackCount: (serverId: string) => number;
  /** The server's latest host snapshot, when it has answered. */
  hostOf: (serverId: string) => HostSnapshot | undefined;
  /** Where a reachable server's tile leads. */
  openHref: (server: FleetServer) => string;
  /** Where an unreachable server's tile leads: its connection settings. */
  fixHref: string;
}

const pct = (used: number, total: number) => (total > 0 ? Math.min(100, Math.round((used / total) * 100)) : null);

function vitals(host: HostSnapshot | undefined): [string, number | null][] {
  if (!host) return [];
  return [
    ['CPU', host.cpu?.percent != null ? Math.min(100, Math.round(host.cpu.percent)) : null],
    ['Mem', host.memory ? pct(host.memory.used, host.memory.total) : null],
    ['Disk', host.disk ? pct(host.disk.total - host.disk.free, host.disk.total) : null],
  ];
}

const statusWord = (s: FleetServer) => (s.status === 'reachable' ? 'Online' : s.status === 'incompatible' ? 'Incompatible version' : 'Unreachable');

/** One tile per server. A server that is not answering shows no numbers: the last ones would read as current. */
export function ServerTiles({ servers, stackCount, hostOf, openHref, fixHref }: ServerTilesProps) {
  return (
    <ul className={styles.grid} aria-label="Servers">
      {servers.map((server) => {
        const online = server.status === 'reachable';
        const host = online ? hostOf(server.id) : undefined;
        const stacks = stackCount(server.id);
        return (
          <li key={server.id} className={styles.item}>
            <Link to={online ? openHref(server) : fixHref} className={styles.tile} data-online={online ? 'true' : 'false'}>
              <span className={styles.top}>
                <b className={styles.name}>{server.name}</b>
                <span className={styles.status}>
                  <Led tone={online ? 'success' : server.status === 'incompatible' ? 'warning' : 'danger'} />
                  {statusWord(server)}
                </span>
              </span>
              {online ? (
                <>
                  {vitals(host).map(([label, value]) =>
                    value === null ? null : (
                      <span key={label} className={styles.vital}>
                        {label}
                        <meter min={0} max={100} value={value} aria-label={`${label} ${value}%`} />
                        {value}%
                      </span>
                    ),
                  )}
                  <span className={styles.meta}>
                    <span>{stacks === 1 ? '1 stack' : `${stacks} stacks`}</span>
                    {host?.containers && <span>{` · ${host.containers.running} of ${host.containers.total} containers running`}</span>}
                  </span>
                </>
              ) : (
                <span className={styles.meta}>
                  {server.lastSeenAt ? (
                    <>
                      No data since <RelativeTime iso={server.lastSeenAt} />
                    </>
                  ) : (
                    'Not answering.'
                  )}
                </span>
              )}
            </Link>
          </li>
        );
      })}
    </ul>
  );
}
