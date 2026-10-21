import { filterLines, type LogLine } from '../logs/buffer';
import type { SSEStatus } from '../sse/client';
import { lineTone } from './logs';

export type LogLevel = 'all' | 'warn' | 'error';

export const LOG_LEVELS: { value: LogLevel; label: string; ariaLabel?: string }[] = [
  { value: 'all', label: 'All', ariaLabel: 'All levels' },
  { value: 'warn', label: 'Warn+' },
  { value: 'error', label: 'Errors' },
];

export function levelMatches(text: string, level: LogLevel): boolean {
  if (level === 'all') return true;
  const tone = lineTone(text);
  return level === 'error' ? tone === 'danger' : tone === 'warning' || tone === 'danger';
}

/** Search then level; service filtering is server-side. */
export function filterConsoleLines(lines: LogLine[], filter: { level: LogLevel; search?: string }): LogLine[] {
  const searched = filterLines(lines, { search: filter.search });
  return filter.level === 'all' ? searched : searched.filter((l) => levelMatches(l.text, filter.level));
}

/** Lines that arrived after the view was frozen by pausing. */
export function newLinesSince(latest: LogLine[], frozenSeq: number): number {
  return Math.max(0, (latest.at(-1)?.seq ?? 0) - frozenSeq);
}

export function searchSegments(text: string, needle: string): { text: string; match: boolean }[] {
  if (!needle) return [{ text, match: false }];
  const lower = text.toLowerCase();
  const n = needle.toLowerCase();
  // toLowerCase can change length (e.g. U+0130); offsets would be wrong, so skip highlighting.
  if (lower.length !== text.length) return [{ text, match: false }];
  const out: { text: string; match: boolean }[] = [];
  let i = 0;
  for (;;) {
    const at = lower.indexOf(n, i);
    if (at < 0) break;
    if (at > i) out.push({ text: text.slice(i, at), match: false });
    out.push({ text: text.slice(at, at + n.length), match: true });
    i = at + n.length;
  }
  if (i < text.length) out.push({ text: text.slice(i), match: false });
  return out;
}

export const LOGS_HEIGHT_KEY = 'stackstead.logsHeight';
export const LOGS_HEIGHT_MIN = 120;
export const LOGS_HEIGHT_MAX = 560;
export const LOGS_HEIGHT_DEFAULT = 240;
export const LOGS_HEIGHT_MOBILE = 280;

/** Leave room for the shell and console controls, including on short landscape screens. */
export function logsHeightBounds(viewportHeight?: number) {
  if (viewportHeight === undefined || !Number.isFinite(viewportHeight) || viewportHeight <= 0) {
    return { min: LOGS_HEIGHT_MIN, max: LOGS_HEIGHT_MAX, initial: LOGS_HEIGHT_DEFAULT };
  }
  const max = Math.max(0, Math.floor(Math.min(viewportHeight * 0.8, viewportHeight - 160)));
  const min = Math.min(LOGS_HEIGHT_MIN, max);
  return { min, max, initial: Math.min(max, Math.max(min, Math.round(viewportHeight * 0.4))) };
}

export function clampLogsHeight(value: unknown, viewportHeight?: number): number {
  const { min, max, initial } = logsHeightBounds(viewportHeight);
  const n = typeof value === 'number' ? value : typeof value === 'string' && value.trim() !== '' ? Number(value) : NaN;
  if (!Number.isFinite(n)) return initial;
  return Math.min(max, Math.max(min, n));
}

/** Read the preference without clamping it to this window, so it survives shrinking and reopening. */
export function readLogsHeight(storage: Pick<Storage, 'getItem'> | undefined): number | undefined {
  try {
    const stored = storage?.getItem(LOGS_HEIGHT_KEY);
    const height = stored?.trim() ? Number(stored) : NaN;
    return Number.isFinite(height) && height >= 0 ? height : undefined;
  } catch {
    return undefined;
  }
}

export function writeLogsHeight(storage: Pick<Storage, 'setItem'> | undefined, height: number): void {
  try {
    if (Number.isFinite(height) && height >= 0) storage?.setItem(LOGS_HEIGHT_KEY, String(Math.round(height)));
  } catch {
    // Private mode or blocked storage: the height just does not persist.
  }
}

export function logClock(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  // Fixed 24-hour HH:MM:SS: the locale's 12-hour form ("9:57:35 PM") would wrap in the time column.
  return [d.getHours(), d.getMinutes(), d.getSeconds()].map((n) => String(n).padStart(2, '0')).join(':');
}

export function logUtc(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toISOString();
}

export function consoleStatus(input: {
  status: SSEStatus;
  ended: boolean;
  paused: boolean;
  sourceKind: 'stack' | 'container';
  /** The followed container's Docker state, when known: an open stream of a stopped container is waiting, not live. */
  containerState?: string;
}): { label: string; tone: 'live' | 'warning' | 'muted' } {
  const { status, ended, paused, sourceKind, containerState } = input;
  if (ended) return { label: 'ended', tone: 'muted' };
  switch (status) {
    case 'open':
      if (paused) return { label: 'paused', tone: 'warning' };
      if (containerState !== undefined && containerState !== 'running') {
        return { label: `waiting - container ${containerState}`, tone: 'warning' };
      }
      return { label: 'live', tone: 'live' };
    case 'connecting':
    case 'reconnecting':
      return { label: status, tone: 'warning' };
    case 'unauthorized':
      return { label: 'session expired - sign in again', tone: 'muted' };
    case 'gone':
      return { label: `${sourceKind} or logs no longer available`, tone: 'muted' };
    default:
      return { label: 'closed', tone: 'muted' };
  }
}
