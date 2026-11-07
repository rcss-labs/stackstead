import type { BackupConfig, BackupRun, BackupStack } from '$lib/api/backupTypes';
import { formatBytes, relativeTime } from '$lib/format';

/** What the screen knows about /api/backups/runs: still loading, unreadable, or newest-first runs. */
export type RunsState = { kind: 'loading' } | { kind: 'error'; message: string } | { kind: 'ready'; runs: BackupRun[] };

export type Tone = 'success' | 'warning' | 'danger' | 'muted';

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
const failedItems = (run: BackupRun) => run.items.filter((i) => i.status === 'failed').length;

const byNewest = (runs: BackupRun[]) => [...runs].sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt));

/** The page headline, from the latest run only. Never says "succeeded" unless the latest run did. */
export function backupHeadline(
  state: RunsState,
  config: BackupConfig | undefined,
  now: Date,
): { title: string; sub: string; tone: Tone } | undefined {
  if (state.kind === 'loading') return undefined;
  if (state.kind === 'error') {
    return { title: 'Backup status unavailable', sub: `The list of backup runs could not be read: ${state.message}`, tone: 'muted' };
  }
  const next = config?.schedule.enabled && config.nextRun ? `Next run ${new Date(config.nextRun).toLocaleString(undefined, { weekday: 'short', hour: '2-digit', minute: '2-digit' })}` : undefined;
  const [latest] = byNewest(state.runs);
  if (!latest) {
    return {
      title: 'No backup has run yet',
      sub: next ?? (config ? 'The schedule is off. Use Back up now, or set a schedule.' : 'Use Back up now, or set a schedule.'),
      tone: 'muted',
    };
  }
  const when = relativeTime(latest.startedAt, now);
  const join = (...parts: (string | undefined)[]) => parts.filter(Boolean).join(' · ');
  switch (latest.status) {
    case 'running':
      return { title: 'A backup is running', sub: `Started ${when}`, tone: 'muted' };
    case 'failed':
      return { title: `Last backup failed ${when}`, sub: join(latest.error ?? 'No error was recorded.', next), tone: 'danger' };
    case 'partial': {
      const n = failedItems(latest);
      return {
        title: `Last backup finished with problems ${when}`,
        sub: join(n > 0 ? `${plural(n, 'item', 'items')} failed` : latest.error ?? 'Some items were not backed up.', next),
        tone: 'warning',
      };
    }
    default:
      return {
        title: `Last backup succeeded ${when}`,
        sub: join(next, latest.size > 0 ? formatBytes(latest.size) : undefined, latest.destinations.join(', ') || undefined),
        tone: 'success',
      };
  }
}

export interface StackCoverage {
  tone: Tone;
  /** Short word for the badge. */
  label: string;
  /** One sentence; states only what settings or the last finished run show. */
  detail: string;
  /** When the last finished run backed this stack up, if it did. */
  lastIso?: string;
}

/**
 * Coverage of one stack from its backup settings plus the last finished run's items.
 * Settings say what is asked for; only a run's items confirm it. Without a readable run the
 * result is never "covered".
 */
export function stackCoverage(row: BackupStack, state: RunsState): StackCoverage {
  const asked = ['files', row.volumes && 'volumes', row.images && 'images'].filter(Boolean).join(', ');
  const finished = state.kind === 'ready' ? byNewest(state.runs).find((r) => r.status !== 'running') : undefined;
  if (!finished) {
    return { tone: 'muted', label: 'Unconfirmed', detail: `Set to back up ${asked}. Not confirmed by a run yet.` };
  }
  const mine = finished.items.filter((i) => i.stack === row.stack);
  if (mine.length === 0) {
    return { tone: 'warning', label: 'Not in last run', detail: `Not in the last run. Set to back up ${asked}.` };
  }
  // The runner names a volume item after the mount's source, the same name this list carries.
  // Named volumes are counted on their own. A bind may be a directory (copied as a volume item),
  // a file in the stack folder (saved with the stack files, no item) or a file outside it (skipped),
  // and the list cannot tell them apart, so a bind is only confirmed or flagged by its own item.
  const volumeItem = (name: string) => mine.find((i) => i.kind === 'volume' && i.name === name);
  const volumes = row.mounts.filter((m) => m.kind === 'volume');
  const binds = row.mounts.filter((m) => m.kind !== 'volume');
  const covered = volumes.filter((m) => volumeItem(m.name)?.status === 'ok').length;
  const bindsCopied = binds.filter((m) => volumeItem(m.name)?.status === 'ok').length;
  const bindsSkipped = binds.filter((m) => volumeItem(m.name)?.status === 'skipped').length;
  const bindsUnconfirmed = binds.length - bindsCopied - bindsSkipped - binds.filter((m) => volumeItem(m.name)?.status === 'failed').length;
  const failed = mine.filter((i) => i.status === 'failed').length;
  const notes: string[] = [];
  if (volumes.length > 0) notes.push(`${covered} of ${plural(volumes.length, 'volume', 'volumes')} covered`);
  if (bindsCopied > 0) notes.push(`${bindsCopied} of ${plural(binds.length, 'bind mount', 'bind mounts')} copied`);
  if (bindsSkipped > 0) notes.push(`${plural(bindsSkipped, 'bind mount', 'bind mounts')} not backed up`);
  if (bindsUnconfirmed > 0) notes.push(`${plural(bindsUnconfirmed, 'bind mount', 'bind mounts')} not confirmed`);
  if (failed > 0) notes.push(`${plural(failed, 'item', 'items')} failed in the last run`);
  const hasFiles = mine.some((i) => i.kind === 'stack-files' && i.status === 'ok');
  if (!hasFiles) notes.push('files not confirmed');
  const gap = failed > 0 || covered < volumes.length || bindsSkipped > 0;
  const lastIso = hasFiles || covered > 0 ? finished.startedAt : undefined;
  const detail = notes.length ? notes.join(' · ') : 'Files covered';
  const unconfirmed = !hasFiles || bindsUnconfirmed > 0;
  if (gap) return { tone: 'warning', label: 'Partly covered', detail, lastIso };
  if (unconfirmed) return { tone: 'muted', label: 'Unconfirmed', detail, lastIso };
  return { tone: 'success', label: 'Covered', detail: volumes.length + binds.length > 0 ? `Files and ${detail}` : detail, lastIso };
}
