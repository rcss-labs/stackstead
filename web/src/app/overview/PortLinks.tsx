import { portChips } from '$lib/stacks/summary';
import styles from './PortLinks.module.css';

/** The first host port of a published port or range, e.g. "9000-9005" opens :9000. */
const firstPort = (port: string) => port.split('-')[0];

/**
 * Published ports, at most three and a +N with the full list as its title. They link to `host` in a new tab;
 * without a host (a server whose application address is not known here) they are plain text, never a link
 * to some other machine's port.
 */
export function PortLinks({ ports, host }: { ports: string[]; host?: string }) {
  if (ports.length === 0) return <span className={styles.none}>—</span>;
  const { shown, more, title } = portChips(ports);
  return (
    <span className={styles.ports}>
      {shown.map((port) =>
        host === undefined ? (
          <span key={port} className={styles.port}>
            :{port}
          </span>
        ) : (
          <a key={port} className={styles.port} href={`http://${host}:${firstPort(port)}`} target="_blank" rel="noreferrer" onClick={(e) => e.stopPropagation()}>
            :{port}
          </a>
        ),
      )}
      {more > 0 && (
        <span className={styles.more} title={title}>
          +{more}
        </span>
      )}
    </span>
  );
}
