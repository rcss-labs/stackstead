import { portChips } from '$lib/stacks/summary';
import styles from './PortLinks.module.css';

/** The first host port of a published port or range, e.g. "9000-9005" opens :9000. */
const firstPort = (port: string) => port.split('-')[0];

/** Published ports as links to the host in a new tab, at most three and a +N with the full list as its title. */
export function PortLinks({ ports }: { ports: string[] }) {
  if (ports.length === 0) return <span className={styles.none}>—</span>;
  const { shown, more, title } = portChips(ports);
  return (
    <span className={styles.ports}>
      {shown.map((port) => (
        <a
          key={port}
          className={styles.port}
          href={`http://${window.location.hostname}:${firstPort(port)}`}
          target="_blank"
          rel="noreferrer"
          onClick={(e) => e.stopPropagation()}
        >
          :{port}
        </a>
      ))}
      {more > 0 && (
        <span className={styles.more} title={title}>
          +{more}
        </span>
      )}
    </span>
  );
}
