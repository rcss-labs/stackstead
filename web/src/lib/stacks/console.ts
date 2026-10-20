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

export function clampLogsHeight(value: unknown): number {
  const n = typeof value === 'number' ? value : typeof value === 'string' && value.trim() !== '' ? Number(value) : NaN;
  if (!Number.isFinite(n)) return LOGS_HEIGHT_DEFAULT;
  return Math.min(LOGS_HEIGHT_MAX, Math.max(LOGS_HEIGHT_MIN, n));
}

export function readLogsHeight(storage: Pick<Storage, 'getItem'> | undefined): number {
  try {
    return clampLogsHeight(storage?.getItem(LOGS_HEIGHT_KEY));
  } catch {
    return LOGS_HEIGHT_DEFAULT;
  }
}

export function writeLogsHeight(storage: Pick<Storage, 'setItem'> | undefined, height: number): void {
  try {
    storage?.setItem(LOGS_HEIGHT_KEY, String(clampLogsHeight(height)));
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
}): { label: string; tone: 'live' | 'warning' | 'muted' } {
  const { status, ended, paused, sourceKind } = input;
  if (ended) return { label: 'ended', tone: 'muted' };
  switch (status) {
    case 'open':
      return paused ? { label: 'paused', tone: 'warning' } : { label: 'live', tone: 'live' };
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
