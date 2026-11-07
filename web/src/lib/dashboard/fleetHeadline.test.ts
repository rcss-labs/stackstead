import { describe, expect, it } from 'vitest';
import type { StackSummary } from '../api/types';
import { fleetHeadline } from './fleetHeadline';

const zero = { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 };
const stack = (name: string, over: Partial<StackSummary> = {}): StackSummary => ({
  name,
  path: `/srv/${name}/compose.yaml`,
  project: `proj-${name}`,
  origin: 'managed',
  status: 'running',
  serviceCount: 1,
  running: 1,
  health: zero,
  ports: [],
  libraryUpdates: 0,
  ...over,
});
const bad = (name: string) => stack(name, { status: 'partial' });

describe('fleetHeadline', () => {
  it('counts reachable servers and says what is hidden', () => {
    const ok = { status: 'reachable' };
    expect(fleetHeadline([ok, ok], [stack('a')])).toEqual({ title: 'All 2 servers are reachable', sub: '1 stack' });
    const h = fleetHeadline([ok, ok, { status: 'unreachable' }], [bad('a'), stack('b')]);
    expect(h.title).toBe('2 of 3 servers are reachable');
    expect(h.sub).toBe('2 stacks · 1 needs attention · stacks on servers that are not answering are hidden');
  });
});
