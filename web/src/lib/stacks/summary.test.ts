import { describe, expect, it } from 'vitest';
import type { StackSummary } from '$lib/api/types';
import { chipCounts, filterStacks, healthCell, portChips } from './summary';

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
  ...over,
});

describe('healthCell', () => {
  it('is empty when every count is zero', () => {
    expect(healthCell(zero).empty).toBe(true);
  });
  it('counts healthy+none as good and unhealthy+failed as bad', () => {
    const c = healthCell({ healthy: 2, unhealthy: 1, starting: 3, none: 1, failed: 2 });
    expect(c).toMatchObject({ empty: false, good: 3, bad: 3, starting: 3 });
    expect(c.title).toBe('2 healthy, 1 unhealthy, 3 starting, 1 no healthcheck, 2 failed');
  });
});

describe('portChips', () => {
  it('hides nothing for three or fewer', () => {
    expect(portChips(['80', '443', '8080'])).toMatchObject({ shown: ['80', '443', '8080'], more: 0 });
  });
  it('shows three and counts the rest', () => {
    const p = portChips(['1', '2', '3', '4', '5']);
    expect(p.shown).toEqual(['1', '2', '3']);
    expect(p.more).toBe(2);
    expect(p.title).toBe('1, 2, 3, 4, 5');
  });
});

describe('filterStacks', () => {
  const items = [stack('alpha'), stack('beta', { status: 'stopped', project: 'shared-db', path: '/data/x/compose.yaml' })];
  it('combines chip and query', () => {
    expect(filterStacks(items, 'all', '').map((s) => s.name)).toEqual(['alpha', 'beta']);
    expect(filterStacks(items, 'stopped', '').map((s) => s.name)).toEqual(['beta']);
    expect(filterStacks(items, 'running', 'beta')).toEqual([]);
  });
  it('matches name, project and path case-insensitively', () => {
    expect(filterStacks(items, 'all', 'ALP').map((s) => s.name)).toEqual(['alpha']);
    expect(filterStacks(items, 'all', 'shared').map((s) => s.name)).toEqual(['beta']);
    expect(filterStacks(items, 'all', '/DATA/').map((s) => s.name)).toEqual(['beta']);
  });
});

describe('chipCounts', () => {
  it('counts per status and all', () => {
    expect(chipCounts([stack('a'), stack('b', { status: 'stopped' }), stack('c')])).toEqual({
      all: 3,
      running: 2,
      partial: 0,
      stopped: 1,
      unavailable: 0,
      unknown: 0,
    });
  });
});
