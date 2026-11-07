import type { ContainerRow, StackSummary } from '../api/types';

export interface PortRow {
  /** First (or only) host port. */
  port: number;
  /** Last port of a range; equals `port` otherwise. */
  last: number;
  /** Set for stack-owned ports; absent for unmanaged containers. */
  stack?: string;
  service?: string;
  container?: string;
  proto?: string;
  bind?: string;
  /** Container state, or the stack status for a port only declared in the compose file. */
  state: string;
}

const PUBLISHED = /^(.+):(\d+)->\d+\/(\w+)$/;

function declared(stack: StackSummary, label: string): PortRow | undefined {
  const [a, b] = label.split('-');
  const port = Number(a);
  if (!Number.isInteger(port)) return undefined;
  const last = b === undefined ? port : Number(b);
  return { port, last: Number.isInteger(last) ? last : port, stack: stack.name, state: stack.status };
}

/**
 * Published ports from running containers (stack-owned ones carry their service, others have no stack),
 * plus ports a stack declares that no container of that stack is publishing, such as when it is stopped.
 * Sorted by port number.
 */
export function portRows(stacks: StackSummary[], containers: ContainerRow[]): PortRow[] {
  const rows: PortRow[] = [];
  for (const c of containers) {
    for (const text of c.ports) {
      const m = PUBLISHED.exec(text);
      if (!m) continue;
      const port = Number(m[2]);
      rows.push({ port, last: port, bind: m[1], proto: m[3], container: c.name, state: c.state, ...(c.stack ? { stack: c.stack, service: c.service || undefined } : {}) });
    }
  }
  for (const s of stacks) {
    for (const label of s.ports) {
      const row = declared(s, label);
      if (!row) continue;
      const live = rows.some((r) => r.stack === s.name && r.port >= row.port && r.port <= row.last);
      if (!live) rows.push(row);
    }
  }
  return rows.sort((a, b) => a.port - b.port || a.last - b.last);
}

/** The smallest port above `from` that no row (or range) uses; 0 when none is left. */
export function nextFreePort(rows: PortRow[], from: number): number {
  for (let p = Math.max(from + 1, 1); p <= 65535; p++) {
    if (!rows.some((r) => p >= r.port && p <= r.last)) return p;
  }
  return 0;
}
