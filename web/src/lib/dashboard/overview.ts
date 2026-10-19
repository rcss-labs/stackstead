// Pure derivations behind the React overview: status panel, towers, groups. Plain values in, plain values out.
import type { HostSnapshot, StackSummary } from '../api/types';
import { attentionCount, attentionStacks, filterStacks, stackNeedsAttention } from '../stacks/summary';

export type PanelTone = 'danger' | 'success' | 'muted';

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;
const allUnknown = (items: StackSummary[]) => items.length > 0 && items.every((s) => s.status === 'unknown');
const runningCount = (items: StackSummary[]) => items.filter((s) => s.status === 'running' && !stackNeedsAttention(s)).length;

export function panelLabel(items: StackSummary[]): { text: string; tone: PanelTone; pulse: boolean } {
  if (items.length === 0) return { text: 'no stacks', tone: 'muted', pulse: false };
  if (allUnknown(items)) return { text: 'status unknown', tone: 'muted', pulse: false };
  const attention = attentionCount(items);
  if (attention > 0) return { text: `degraded · ${plural(attention, 'stack')}`, tone: 'danger', pulse: true };
  const running = runningCount(items);
  if (running === items.length) return { text: `all running · ${plural(items.length, 'stack')}`, tone: 'success', pulse: false };
  return { text: `healthy · ${running} of ${items.length} running`, tone: 'success', pulse: false };
}

export function statusSentence(items: StackSummary[]): string {
  if (items.length === 0) return 'No stacks yet';
  if (allUnknown(items)) return 'Stack status is unavailable';
  const attention = attentionStacks(items);
  if (attention.length === 1) return `${attention[0].name} needs attention`;
  if (attention.length === 2) return `${attention[0].name} and ${attention[1].name} need attention`;
  if (attention.length > 2) return `${attention[0].name} and ${attention.length - 1} more need attention`;
  return runningCount(items) === items.length ? 'All stacks are running' : 'Nothing needs attention';
}

/** The sub-line pieces the panel can compute; the component appends "last event <relative>". */
export function statusFragments(items: StackSummary[], host: HostSnapshot | undefined): string[] {
  const fragments = [`${runningCount(items)} of ${plural(items.length, 'stack')} fully running`];
  if (host?.containers) fragments.push(`${host.containers.running} of ${host.containers.total} containers up`);
  return fragments;
}

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
