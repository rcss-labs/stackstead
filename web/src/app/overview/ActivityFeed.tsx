import { Link } from 'react-router';
import type { DockerEvent } from '$lib/api/types';
import type { SSEStatus } from '$lib/sse/client';
import { serverPath } from '$lib/servers/fleet';
import styles from './SideColumn.module.css';

import { useServer } from '../servers/ServerContext';
export type EventTone = 'success' | 'warning' | 'danger' | 'muted';

function tone(action: string): EventTone {
  if (['die', 'oom', 'kill', 'unhealthy'].includes(action)) return 'danger';
  if (['stop', 'pause', 'restart'].includes(action)) return 'warning';
  if (['start', 'create', 'healthy'].includes(action)) return 'success';
  return 'muted';
}

// health_status events carry the outcome in `health`; show that instead of the raw action.
const actionOf = (e: DockerEvent) => (e.action === 'health_status' && e.health ? e.health : e.action);

function clock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const two = (n: number) => String(n).padStart(2, '0');
  return `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
}

const subjectOf = (e: DockerEvent) => (e.action === 'die' && e.exitCode !== undefined ? `${e.container} exit ${e.exitCode}` : e.container);

export interface ActivityFeedProps {
  /** Newest first; the events store already caps the list at 50. */
  events: DockerEvent[];
  connection: SSEStatus;
  /** Tags each event with its server and links it there; for the combined All stacks feed. */
  serverOf?: (event: DockerEvent) => { name: string; slug: string | null };
}

export function ActivityFeed({ events, connection, serverOf }: ActivityFeedProps) {
  const { href } = useServer();
  const live = connection === 'open';
  return (
    <section className={styles.card}>
      <h2 className={styles.title}>
        Activity
        <span className={styles.status} data-state={live ? 'live' : 'reconnecting'}>
          {live && <i className={styles.dot} aria-hidden="true" />}
          {live ? 'live' : 'reconnecting'}
        </span>
      </h2>
      {events.length === 0 ? (
        <p className={styles.empty}>Waiting for events…</p>
      ) : (
        // Deliberately not a live region: a busy host would make it chatter.
        <ul className={styles.events}>
          {events.map((e, i) => {
            const action = actionOf(e);
            const subject = subjectOf(e);
            const server = serverOf?.(e);
            const containerPath = `/containers/${encodeURIComponent(e.container)}`;
            return (
              <li key={`${e.time}|${e.container}|${e.action}|${i}`} className={styles.event}>
                <span className={styles.tone} data-tone={tone(action)} aria-hidden="true" />
                <span className={styles.what}>
                  <span className={styles.action} data-tone={tone(action)} title={action}>
                    {action}
                  </span>{' '}
                  {e.container ? (
                    <Link className={styles.subject} to={server ? serverPath(server.slug, containerPath) : href(containerPath)} title={subject}>
                      {subject}
                    </Link>
                  ) : (
                    <span className={styles.subject} title={subject}>
                      {subject}
                    </span>
                  )}
                  {server && <span className={styles.server}>{server.name}</span>}
                </span>
                <span className={styles.time}>{clock(e.time)}</span>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
