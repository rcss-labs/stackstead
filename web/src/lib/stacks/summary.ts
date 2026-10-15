import type { HealthCounts, StackStatus, StackSummary } from '$lib/api/types';

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
