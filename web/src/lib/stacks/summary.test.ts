import { describe, expect, it } from 'vitest';
import type { ContainerInfo, ServiceDetail, StackSummary } from '$lib/api/types';
import { attentionCount, attentionStacks, chipCounts, filterStacks, healthCell, needsAttention, portChips, serviceState } from './summary';

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

describe('serviceState', () => {
  const ctr = (over: Partial<ContainerInfo> = {}): ContainerInfo => ({
    id: 'c1',
    name: 'web-app-1',
    service: 'app',
    state: 'running',
    status: 'Up 2 minutes',
    health: 'none',
    ...over,
  });
  const svc = (containers: ContainerInfo[]): ServiceDetail => ({ name: 'app', config: { limits: {} }, readOnly: [], containers });

  it.each([
    ['running healthy', [ctr({ health: 'healthy', status: 'Up 2 minutes (healthy)' })], 'success', 'healthy · Up 2 minutes (healthy)'],
    ['running without healthcheck', [ctr()], 'success', 'Up 2 minutes'],
    ['one unhealthy', [ctr(), ctr({ id: 'c2', health: 'unhealthy', status: 'Up 1m (unhealthy)' })], 'danger', 'Up 2 minutes'],
    ['restarting', [ctr({ state: 'restarting', status: 'Restarting (1) 5s ago' })], 'danger', 'Restarting (1) 5s ago'],
    ['dead', [ctr({ state: 'dead', status: 'Dead' })], 'danger', 'Dead'],
    ['exited(1)', [ctr({ state: 'exited', status: 'Exited (1) 3 minutes ago' })], 'danger', 'Exited (1) 3 minutes ago'],
    ['all exited(0)', [ctr({ state: 'exited', status: 'Exited (0) 3 minutes ago' })], 'muted', 'Exited (0) 3 minutes ago'],
    ['starting', [ctr({ health: 'starting', status: 'Up 3s (health: starting)' })], 'warning', 'starting · Up 3s (health: starting)'],
    ['no containers', [], 'muted', 'No containers'],
  ] as const)('%s', (_, containers, tone, text) => {
    expect(serviceState(svc([...containers]))).toEqual({ tone, text });
  });
});

describe('needsAttention', () => {
  const s = (status: StackSummary['status'], h: Partial<typeof zero> = {}) => stack('x', { status, health: { ...zero, ...h } });

  it.each([
    ['running all healthy', s('running', { healthy: 2 }), false],
    ['partial', s('partial'), true],
    ['unhealthy > 0', s('running', { unhealthy: 1 }), true],
    ['failed > 0', s('running', { failed: 1 }), true],
    ['stopped', s('stopped'), false],
    ['unavailable', s('unavailable'), true],
  ])('%s', (_label, item, expected) => {
    expect(needsAttention([item])).toBe(expected);
  });

  it('attentionCount counts matching stacks', () => {
    expect(attentionCount([s('running'), s('partial'), s('unavailable'), s('stopped')])).toBe(2);
    expect(needsAttention([])).toBe(false);
  });
});

describe('attentionStacks', () => {
  it('orders unavailable, partial, then health-only, each by name', () => {
    const items = [
      stack('hz', { health: { ...zero, unhealthy: 1 } }),
      stack('pb', { status: 'partial' }),
      stack('ua', { status: 'unavailable' }),
      stack('ok'),
      stack('ha', { health: { ...zero, failed: 1 } }),
      stack('pa', { status: 'partial' }),
      stack('ub', { status: 'unavailable' }),
    ];
    expect(attentionStacks(items).map((s) => s.name)).toEqual(['ua', 'ub', 'pa', 'pb', 'ha', 'hz']);
  });
});
