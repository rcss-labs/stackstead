import { Link } from 'react-router';
import type { DockerEvent } from '$lib/api/types';
import type { SSEStatus } from '$lib/sse/client';
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
}

export function ActivityFeed({ events, connection }: ActivityFeedProps) {
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
            return (
              <li key={`${e.time}|${e.container}|${e.action}|${i}`} className={styles.event}>
                <span className={styles.tone} data-tone={tone(action)} aria-hidden="true" />
                <span className={styles.what}>
                  <span className={styles.action} data-tone={tone(action)} title={action}>
                    {action}
                  </span>{' '}
                  {e.container ? (
                    <Link className={styles.subject} to={href(`/containers/${encodeURIComponent(e.container)}`)} title={subject}>
                      {subject}
                    </Link>
                  ) : (
                    <span className={styles.subject} title={subject}>
                      {subject}
                    </span>
                  )}
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
