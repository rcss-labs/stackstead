import { useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router';
import { createBackupsApi } from '$lib/api/backups';
import { request } from '$lib/api/client';
import type { Operation } from '$lib/api/types';
import {
  fromActions,
  fromBackupRuns,
  fromEvents,
  fromOperations,
  mergeActivity,
  reachNote,
  readLastVisit,
  sinceLastVisit,
  writeLastVisit,
  type ActivityItem,
  type ActivityKind,
} from '$lib/activity/activity';
import { relativeTime } from '$lib/format';
import { Banner } from '../components/Banner';
import { Chip } from '../components/Chip';
import { EmptyState } from '../components/EmptyState';
import { PanelLabel } from '../components/PanelLabel';
import { Tag } from '../components/Tag';
import { useStore } from '../hooks/useStore';
import { useServer } from '../servers/ServerContext';
import styles from './ActivityScreen.module.css';

const KINDS: { kind: ActivityKind; label: string; source: string }[] = [
  { kind: 'operation', label: 'Operations', source: 'operations' },
  { kind: 'event', label: 'Docker events', source: 'Docker events' },
  { kind: 'action', label: 'Actions', source: 'actions' },
  { kind: 'backup', label: 'Backups', source: 'backup runs' },
];
const KIND_NAME: Record<ActivityKind, string> = { operation: 'Operation', event: 'Docker event', action: 'Action', backup: 'Backup' };

type Source = { items: ActivityItem[]; error?: string } | undefined;

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

export function ActivityScreen() {
  const server = useServer();
  const { api, events, href, id, local } = server;
  const base = local ? '' : `/api/servers/${encodeURIComponent(id)}`;
  // Read once, then record this visit; the summary compares against the previous one.
  const [lastVisit] = useState(readLastVisit);
  useEffect(() => writeLastVisit(Date.now()), []);

  const [fetched, setFetched] = useState<Partial<Record<ActivityKind, Source>>>({});
  useEffect(() => {
    let live = true;
    setFetched({});
    const settle = (kind: ActivityKind, load: Promise<ActivityItem[]>) =>
      load.then(
        (items) => live && setFetched((f) => ({ ...f, [kind]: { items } })),
        (e: unknown) => live && setFetched((f) => ({ ...f, [kind]: { items: [], error: message(e) } })),
      );
    void settle('operation', request<Operation[]>('GET', `${base}/api/operations`).then((r) => fromOperations(r.data)));
    void settle('action', api.recentActions({ limit: 100 }).then(fromActions));
    void settle('backup', createBackupsApi(base).runs(50).then(fromBackupRuns));
    return () => {
      live = false;
    };
  }, [api, base]);

  const liveEvents = useStore(events);
  const eventSource = useMemo<Source>(() => ({ items: fromEvents(liveEvents) }), [liveEvents]);
  const sources = useMemo(() => ({ ...fetched, event: eventSource }) as Record<ActivityKind, Source>, [fetched, eventSource]);

  const [filter, setFilter] = useState<ActivityKind | 'all'>('all');
  const all = useMemo(() => mergeActivity(Object.values(sources).map((s) => s?.items ?? [])), [sources]);
  const shown = filter === 'all' ? all : all.filter((i) => i.kind === filter);
  const loading = KINDS.some((k) => sources[k.kind] === undefined);
  const now = new Date();
  const summary = loading ? '' : sinceLastVisit(all, lastVisit);

  const days: { day: string; items: ActivityItem[] }[] = [];
  for (const item of shown) {
    const day = Number.isNaN(Date.parse(item.time)) ? 'Unknown time' : new Date(item.time).toLocaleDateString(undefined, { weekday: 'long', month: 'short', day: 'numeric' });
    const last = days[days.length - 1];
    if (last?.day === day) last.items.push(item);
    else days.push({ day, items: [item] });
  }

  return (
    <div className={styles.page}>
      <header>
        <h1 className={styles.title}>What happened</h1>
        <p className={styles.sub}>Operations, Docker events, actions and backup runs from {local ? 'this server' : server.name} in one timeline.</p>
      </header>

      {KINDS.map(({ kind, source }) =>
        sources[kind]?.error ? (
          <Banner key={kind} kind="warning" title={`Could not load ${source}`}>
            {sources[kind]?.error}. The rest of the timeline is still shown.
          </Banner>
        ) : null,
      )}

      {summary && (
        <section className={styles.panel} aria-label="Since your last visit">
          <PanelLabel>Since your last visit</PanelLabel>
          <p>{summary}</p>
        </section>
      )}

      <div className={styles.chips} role="group" aria-label="Filter by kind">
        <Chip pressed={filter === 'all'} onClick={() => setFilter('all')}>
          All
        </Chip>
        {KINDS.map(({ kind, label }) => (
          <Chip key={kind} pressed={filter === kind} onClick={() => setFilter(kind)} count={sources[kind]?.items.length}>
            {label}
          </Chip>
        ))}
      </div>

      {days.length === 0 && !loading && <EmptyState title="Nothing to show" description={filter === 'all' ? 'No events, operations, actions or backup runs are recorded right now.' : 'None of this kind in the records below.'} />}
      {days.map(({ day, items }) => (
        <section key={day} className={styles.panel}>
          <PanelLabel as="h2">{day}</PanelLabel>
          <ul className={styles.list}>
            {items.map((item) => (
              <li key={item.id}>
                <Link to={href(item.path)} className={styles.row}>
                  <Tag tone={item.tone === 'danger' || item.tone === 'warning' || item.tone === 'success' ? item.tone : 'muted'}>{KIND_NAME[item.kind]}</Tag>
                  <span className={styles.what}>
                    <span className={styles.name}>{item.title}</span>
                    {item.detail && <span className={styles.detail}>{item.detail}</span>}
                  </span>
                  <time className={styles.time} dateTime={item.time} title={new Date(item.time).toLocaleString()}>
                    {relativeTime(item.time, now)}
                  </time>
                </Link>
              </li>
            ))}
          </ul>
        </section>
      ))}

      <section className={styles.panel} aria-label="How far back this goes">
        <PanelLabel as="h2">How far back this goes</PanelLabel>
        <ul className={styles.reach}>
          {KINDS.map(({ kind, source }) => (
            <li key={kind}>{sources[kind]?.error ? `${source}: unavailable.` : sources[kind] ? reachNote(kind, sources[kind].items, now) : `${source}: loading…`}</li>
          ))}
        </ul>
        <p className={styles.sub}>Docker events are not stored durably, so older container history is not available here.</p>
      </section>
    </div>
  );
}

export default ActivityScreen;
