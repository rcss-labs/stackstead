// Pure helpers for the Containers list plus the controller that keeps it fresh (spec §5.2):
// refetch on every live change (debounced) and every 10 s while the tab is visible, nothing while hidden.
import { type Readable } from '$lib/store';
import type { ContainerRow } from '../api/types';
import { createRefreshList } from '../stores/refreshList';
import type { LiveChange } from '../stores/live';

export type ContainerChip = 'all' | 'running' | 'stopped' | 'problems' | 'unmanaged';


export function isProblem(c: ContainerRow): boolean {
  if (c.state === 'restarting' || c.state === 'dead' || c.health === 'unhealthy') return true;
  return c.state === 'exited' && c.exitCode !== null && c.exitCode !== 0;
}

const rank = (c: ContainerRow) => (isProblem(c) ? 0 : c.state === 'running' ? 1 : 2);

export function sortContainers(rows: ContainerRow[]): ContainerRow[] {
  return [...rows].sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
}

export function filterContainers(rows: ContainerRow[], chip: ContainerChip, query: string): ContainerRow[] {
  const q = query.trim().toLowerCase();
  return rows.filter((c) => {
    if (chip === 'running' && c.state !== 'running') return false;
    if (chip === 'stopped' && c.state === 'running') return false;
    if (chip === 'problems' && !isProblem(c)) return false;
    if (chip === 'unmanaged' && c.managed === 'stack') return false;
    if (!q) return true;
    return [c.name, c.image, c.stack, c.project, ...c.ports].some((field) => field.toLowerCase().includes(q));
  });
}

export function chipCounts(rows: ContainerRow[]): Record<ContainerChip, number> {
  const counts: Record<ContainerChip, number> = { all: rows.length, running: 0, stopped: 0, problems: 0, unmanaged: 0 };
  for (const c of rows) {
    if (c.state === 'running') counts.running++;
    else counts.stopped++;
    if (isProblem(c)) counts.problems++;
    if (c.managed !== 'stack') counts.unmanaged++;
  }
  return counts;
}

export interface ContainerListDeps {
  list: () => Promise<ContainerRow[]>;
  changes: Readable<LiveChange | null>;
  doc?: Document;
}

export function createContainerList(deps: ContainerListDeps) {
  return createRefreshList({ ...deps, key: (row) => row.id });
}
