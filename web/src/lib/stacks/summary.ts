import type { ContainerInfo, HealthCounts, ServiceDetail, StackStatus, StackSummary } from '$lib/api/types';

export function healthCell(h: HealthCounts): { empty: boolean; good: number; bad: number; starting: number; title: string } {
  const empty = h.healthy + h.unhealthy + h.starting + h.none + h.failed === 0;
  return {
    empty,
    good: h.healthy + h.none,
    bad: h.unhealthy + h.failed,
    starting: h.starting,
    title: `${h.healthy} healthy, ${h.unhealthy} unhealthy, ${h.starting} starting, ${h.none} no healthcheck, ${h.failed} failed`,
  };
}

export function portChips(ports: string[], max = 3): { shown: string[]; more: number; title: string } {
  return { shown: ports.slice(0, max), more: Math.max(0, ports.length - max), title: ports.join(', ') };
}

export type ChipFilter = 'all' | StackStatus;

export function filterStacks(items: StackSummary[], chip: ChipFilter, query: string): StackSummary[] {
  const q = query.trim().toLowerCase();
  return items.filter(
    (s) =>
      (chip === 'all' || s.status === chip) &&
      (!q || s.name.toLowerCase().includes(q) || s.project.toLowerCase().includes(q) || s.path.toLowerCase().includes(q)),
  );
}

export function chipCounts(items: StackSummary[]): Record<ChipFilter, number> {
  const counts: Record<ChipFilter, number> = { all: items.length, running: 0, partial: 0, stopped: 0, unavailable: 0, unknown: 0 };
  for (const s of items) counts[s.status]++;
  return counts;
}

export type StateTone = 'success' | 'danger' | 'warning' | 'muted';

const exitedNonZero = (c: ContainerInfo) => c.state === 'exited' && !/\(0\)/.test(c.status);

/** One dot and one line of text for a service, from its containers. */
export function serviceState(svc: ServiceDetail): { tone: StateTone; text: string } {
  const cs = svc.containers;
  if (cs.length === 0) return { tone: 'muted', text: 'No containers' };
  const first = cs[0];
  const text = first.health === 'none' ? first.status : `${first.health} · ${first.status}`;
  const bad = cs.some(
    (c) => c.health === 'unhealthy' || c.state === 'restarting' || c.state === 'dead' || exitedNonZero(c),
  );
  if (bad) return { tone: 'danger', text };
  if (cs.some((c) => c.health === 'starting')) return { tone: 'warning', text };
  if (cs.some((c) => c.state === 'running')) return { tone: 'success', text };
  return { tone: 'muted', text };
}

/** A stack needs attention when it is partial or unavailable, or has an unhealthy or failed container. */
export function stackNeedsAttention(s: StackSummary): boolean {
  return s.status === 'partial' || s.status === 'unavailable' || s.health.unhealthy > 0 || s.health.failed > 0;
}

export function needsAttention(items: StackSummary[]): boolean {
  return items.some(stackNeedsAttention);
}

export function attentionCount(items: StackSummary[]): number {
  return items.filter(stackNeedsAttention).length;
}

const attentionRank = (s: StackSummary) => (s.status === 'unavailable' ? 0 : s.status === 'partial' ? 1 : 2);

/** Stacks that need attention: unavailable first, then partial, then health-only; each by name. */
export function attentionStacks(items: StackSummary[]): StackSummary[] {
  return items
    .filter(stackNeedsAttention)
    .sort((a, b) => attentionRank(a) - attentionRank(b) || a.name.localeCompare(b.name));
}
