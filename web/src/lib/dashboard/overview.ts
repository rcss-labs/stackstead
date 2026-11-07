// Pure derivations behind the React overview: status panel, towers, groups. Plain values in, plain values out.
import type { Alert, HostSnapshot, StackSummary } from '../api/types';
import type { LiveState } from '../stores/live';
import { EXCLUDED_ALERT_KINDS } from './issues';
import { attentionStacks, filterStacks, stackNeedsAttention } from '../stacks/summary';

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
const allUnknown = (items: StackSummary[]) => items.length > 0 && items.every((s) => s.status === 'unknown');
const runningCount = (items: StackSummary[]) => items.filter((s) => s.status === 'running' && !stackNeedsAttention(s)).length;

export type TowerBlock = 'up' | 'unhealthy' | 'failed' | 'down' | 'off';

/** One block per service, bottom-up. Counts come from independent tallies, so each is clamped to what is left. */
export function towerBlocks(s: StackSummary): TowerBlock[] {
  const total = Math.max(0, s.serviceCount);
  const unhealthy = Math.min(total, Math.max(0, s.health.unhealthy));
  const failed = Math.min(total - unhealthy, Math.max(0, s.health.failed));
  const remaining = total - unhealthy - failed;
  const up = Math.min(remaining, Math.max(0, s.running - unhealthy));
  const rest: TowerBlock = s.status === 'stopped' ? 'off' : 'down';
  return [
    ...Array<TowerBlock>(up).fill('up'),
    ...Array<TowerBlock>(unhealthy).fill('unhealthy'),
    ...Array<TowerBlock>(failed).fill('failed'),
    ...Array<TowerBlock>(remaining - up).fill(rest),
  ];
}

export function towerLabelTone(s: StackSummary): 'warning' | 'faint' | 'muted' {
  if (stackNeedsAttention(s)) return 'warning';
  return s.status === 'stopped' ? 'faint' : 'muted';
}

export const towerTitle = (s: StackSummary): string => `${s.name} · ${s.status}`;

export type StackGroupId = 'attention' | 'running' | 'stopped' | 'unknown';

export interface StackGroup {
  id: StackGroupId;
  title: 'Needs attention' | 'Running' | 'Stopped' | 'Unknown';
  items: StackSummary[];
}

const GROUPS: { id: StackGroupId; title: StackGroup['title'] }[] = [
  { id: 'attention', title: 'Needs attention' },
  { id: 'running', title: 'Running' },
  { id: 'stopped', title: 'Stopped' },
  { id: 'unknown', title: 'Unknown' },
];

function groupOf(s: StackSummary): StackGroupId {
  if (stackNeedsAttention(s)) return 'attention';
  if (s.status === 'running') return 'running';
  return s.status === 'stopped' ? 'stopped' : 'unknown';
}

export function groupStacks(items: StackSummary[]): StackGroup[] {
  const sorted = [...items].sort((a, b) => a.name.localeCompare(b.name));
  return GROUPS.map((g) => ({ ...g, items: sorted.filter((s) => groupOf(s) === g.id) })).filter((g) => g.items.length > 0);
}

export function rowPrimaryAction(s: StackSummary): { action: 'up' | 'restart'; label: 'Up' | 'Restart' } {
  return s.status === 'running' || s.status === 'partial' ? { action: 'restart', label: 'Restart' } : { action: 'up', label: 'Up' };
}

export const filterByQuery = (items: StackSummary[], query: string): StackSummary[] => filterStacks(items, 'all', query);

const issueWord =(n: number, noun: 'stack' | 'issue') => `${n} ${noun}${n === 1 ? ' needs' : 's need'} attention`;

/**
 * The page's one-line verdict. It says "running" only when stacks, alerts, Docker and the live
 * connection all support it; anything missing yields "Health is not confirmed" instead.
 */
export function headline(
  items: StackSummary[],
  alerts: Alert[] | undefined,
  live: Pick<LiveState, 'docker' | 'connection'>,
  host?: Pick<HostSnapshot, 'containers'>,
): { title: string; sub: string } {
  if (live.docker === 'unavailable') {
    return { title: 'Docker is not responding', sub: 'Stackstead is still running. Your containers may be fine; it just cannot see them.' };
  }
  if (items.length === 0) return { title: 'No stacks yet', sub: '' };
  if (allUnknown(items)) return { title: 'Stack status is unavailable', sub: '' };

  const counts = [`${runningCount(items)} of ${plural(items.length, 'stack')} fully running`];
  if (host?.containers) counts.push(`${host.containers.running} of ${host.containers.total} containers running`);
  const sub = counts.join(' · ');

  const flagged = (alerts ?? []).filter((a) => !EXCLUDED_ALERT_KINDS.has(a.kind) && a.severity !== 'info');
  const stacks = new Set([...attentionStacks(items).map((s) => s.name), ...flagged.flatMap((a) => (a.stack ? [a.stack] : []))]);
  if (stacks.size > 0) return { title: issueWord(stacks.size, 'stack'), sub };
  const loose = flagged.filter((a) => !a.stack).length;
  if (loose > 0) return { title: issueWord(loose, 'issue'), sub };

  const unconfirmed = !alerts ? 'Alerts have not loaded.' : live.docker !== 'ok' ? 'Docker has not answered yet.' : live.connection !== 'open' ? 'Live updates are disconnected.' : '';
  if (unconfirmed) return { title: 'Health is not confirmed', sub: unconfirmed };
  return { title: runningCount(items) === items.length ? 'All stacks are running' : 'Nothing needs attention', sub };
}
