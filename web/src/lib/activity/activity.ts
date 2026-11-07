import type { BackupRun } from '../api/backupTypes';
import type { ActionEntry, DockerEvent, Operation } from '../api/types';
import { relativeTime } from '../format';

export type ActivityKind = 'operation' | 'event' | 'action' | 'backup';
export type ActivityTone = 'success' | 'warning' | 'danger' | 'muted';

export interface ActivityItem {
  id: string;
  kind: ActivityKind;
  /** RFC 3339. */
  time: string;
  title: string;
  detail: string;
  tone: ActivityTone;
  /** App path; the screen passes it through href(). */
  path: string;
}

const ms = (iso: string) => {
  const t = Date.parse(iso);
  return Number.isNaN(t) ? -Infinity : t;
};

/** All sources in one list, newest first; entries with an unreadable time go last. */
export function mergeActivity(sources: ActivityItem[][]): ActivityItem[] {
  return sources.flat().sort((a, b) => ms(b.time) - ms(a.time));
}

const seg = encodeURIComponent;

const eventTone = (action: string): ActivityTone =>
  ['die', 'oom', 'kill', 'unhealthy'].includes(action) ? 'danger' : ['stop', 'pause', 'restart'].includes(action) ? 'warning' : ['start', 'create', 'healthy'].includes(action) ? 'success' : 'muted';

export function fromEvents(events: DockerEvent[]): ActivityItem[] {
  return events.map((e, i) => {
    const action = e.action === 'health_status' && e.health ? e.health : e.action;
    return {
      id: `event:${e.time}:${e.container}:${e.action}:${i}`,
      kind: 'event',
      time: e.time,
      title: `${e.container || 'container'} ${action}`,
      detail: e.action === 'die' && e.exitCode !== undefined ? `exit code ${e.exitCode}` : e.image,
      tone: eventTone(action),
      path: e.container ? `/containers/${seg(e.container)}` : '/containers',
    };
  });
}

const opTone = (state: Operation['state']): ActivityTone => (state === 'succeeded' ? 'success' : state === 'running' || state === 'cancelled' ? 'muted' : state === 'timed_out' ? 'warning' : 'danger');

export function fromOperations(ops: Operation[]): ActivityItem[] {
  return ops.map((o) => ({
    id: `operation:${o.id}`,
    kind: 'operation',
    time: o.startedAt,
    title: `${o.action} ${o.stack}${o.service ? `/${o.service}` : ''}`,
    detail: o.state.replace('_', ' ') + (o.error ? `: ${o.error}` : ''),
    tone: opTone(o.state),
    path: `/stacks/${seg(o.stack)}`,
  }));
}

function actionPath(a: ActionEntry): string {
  if (a.kind.startsWith('backup.')) return '/backups';
  if (a.kind === 'container.exec' && a.target) return `/containers/${seg(a.target)}`;
  if (a.kind.startsWith('storage.') || a.kind.startsWith('mount.')) return '/storage';
  return a.scope && a.scope !== 'host' ? `/stacks/${seg(a.scope)}` : '/';
}

export function fromActions(actions: ActionEntry[]): ActivityItem[] {
  return actions.map((a) => ({
    id: `action:${a.id}`,
    kind: 'action',
    time: a.ts,
    title: a.target ? `${a.kind} ${a.target}` : a.kind,
    detail: [a.username && `by ${a.username}`, a.result, a.detail].filter(Boolean).join(' · '),
    tone: a.result === 'ok' || a.result === 'success' ? 'success' : a.result === '' ? 'muted' : 'danger',
    path: actionPath(a),
  }));
}

const runTone = (s: BackupRun['status']): ActivityTone => (s === 'success' ? 'success' : s === 'partial' ? 'warning' : s === 'failed' ? 'danger' : 'muted');

export function fromBackupRuns(runs: BackupRun[]): ActivityItem[] {
  return runs.map((r) => ({
    id: `backup:${r.id}`,
    kind: 'backup',
    time: r.startedAt,
    title: `${r.trigger === 'schedule' ? 'Scheduled' : 'Manual'} backup ${r.status}`,
    detail: r.error ?? r.archive ?? '',
    tone: runTone(r.status),
    path: '/backups',
  }));
}

const KIND_LABEL: Record<ActivityKind, [string, string]> = {
  operation: ['operation', 'operations'],
  event: ['Docker event', 'Docker events'],
  action: ['action', 'actions'],
  backup: ['backup run', 'backup runs'],
};

/** Plain-text summary of what is newer than the last visit; '' on a first visit, when there is nothing to compare with. */
export function sinceLastVisit(items: ActivityItem[], lastVisit: number | null): string {
  if (lastVisit === null) return '';
  const counts = new Map<ActivityKind, number>();
  for (const i of items) if (ms(i.time) > lastVisit) counts.set(i.kind, (counts.get(i.kind) ?? 0) + 1);
  const parts = (Object.keys(KIND_LABEL) as ActivityKind[]).filter((k) => counts.has(k)).map((k) => `${counts.get(k)} ${KIND_LABEL[k][counts.get(k) === 1 ? 0 : 1]}`);
  if (parts.length === 0) return 'Nothing new since your last visit.';
  return `${parts.length > 1 ? `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]}` : parts[0]} since your last visit.`;
}

const VISIT_KEY = 'stackstead.activity.lastVisit';

/** Last visit in ms, or null when never visited or storage is unavailable. */
export function readLastVisit(): number | null {
  try {
    const n = Number(localStorage.getItem(VISIT_KEY));
    return Number.isFinite(n) && n > 0 ? n : null;
  } catch {
    return null;
  }
}

export function writeLastVisit(at: number): void {
  try {
    localStorage.setItem(VISIT_KEY, String(at));
  } catch {
    // Private mode or blocked storage: the summary just stays off.
  }
}

const REACH: Record<ActivityKind, string> = {
  event: 'Docker events: only the last 50 the server saw; the list is kept in memory and starts over when the server restarts.',
  operation: 'Operations: finished ones are kept for one hour (up to 20 per stack), then dropped.',
  action: 'Actions: the audit log (storage cleanups, backup restores, container exec, mount fixes), the newest 100.',
  backup: 'Backup runs: the newest 50.',
};

/** How far back one source reaches: its fixed limit plus the oldest entry actually shown. */
export function reachNote(kind: ActivityKind, items: ActivityItem[], now: Date): string {
  const oldest = items.filter((i) => ms(i.time) > -Infinity).sort((a, b) => ms(a.time) - ms(b.time))[0];
  return `${REACH[kind]} ${oldest ? `Oldest shown: ${relativeTime(oldest.time, now)}.` : 'Shown: none right now.'}`;
}
