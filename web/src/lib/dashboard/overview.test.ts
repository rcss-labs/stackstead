import { describe, expect, it } from 'vitest';
import type { Alert, HostSnapshot, StackSummary } from '../api/types';
import {
  filterByQuery,
  fleetHeadline,
  groupStacks,
  headline,
  rowPrimaryAction,
  towerBlocks,
  towerLabelTone,
  towerTitle,
} from './overview';

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

describe('towerBlocks', () => {
  it('running stack 3/3 healthy → three up', () => {
    expect(towerBlocks(stack('a', { serviceCount: 3, running: 3 }))).toEqual(['up', 'up', 'up']);
  });
  it('partial with one unhealthy and one exited → up, unhealthy, down bottom-up', () => {
    const s = stack('a', { status: 'partial', serviceCount: 3, running: 2, health: { ...zero, unhealthy: 1 } });
    expect(towerBlocks(s)).toEqual(['up', 'unhealthy', 'down']);
  });
  it('failed services sit between unhealthy and down', () => {
    const s = stack('a', { status: 'partial', serviceCount: 4, running: 2, health: { ...zero, unhealthy: 1, failed: 1 } });
    expect(towerBlocks(s)).toEqual(['up', 'unhealthy', 'failed', 'down']);
  });
  it('stopped stack → all off', () => {
    expect(towerBlocks(stack('a', { status: 'stopped', serviceCount: 2, running: 0 }))).toEqual(['off', 'off']);
  });
  it('no services → no blocks', () => {
    expect(towerBlocks(stack('a', { serviceCount: 0, running: 0 }))).toEqual([]);
  });
  it('counts that exceed serviceCount are clamped', () => {
    const s = stack('a', { status: 'partial', serviceCount: 2, running: 5, health: { ...zero, unhealthy: 7, failed: 9 } });
    expect(towerBlocks(s)).toEqual(['unhealthy', 'unhealthy']);
    const f = stack('a', { status: 'partial', serviceCount: 2, running: 0, health: { ...zero, failed: 9 } });
    expect(towerBlocks(f)).toEqual(['failed', 'failed']);
    expect(towerBlocks(stack('a', { serviceCount: 2, running: 9 }))).toEqual(['up', 'up']);
  });
  it('label tone and title', () => {
    expect(towerLabelTone(bad('a'))).toBe('warning');
    expect(towerLabelTone(stack('a', { status: 'stopped' }))).toBe('faint');
    expect(towerLabelTone(stack('a'))).toBe('muted');
    expect(towerTitle(bad('a'))).toBe('a · partial');
  });
});

describe('groupStacks', () => {
  it('puts health-only attention in Needs attention, omits empty groups and sorts by name', () => {
    const groups = groupStacks([
      stack('z-run'),
      stack('b-sick', { health: { ...zero, unhealthy: 1 } }),
      stack('a-run'),
      stack('m-off', { status: 'stopped' }),
      stack('a-part', { status: 'partial' }),
    ]);
    expect(groups.map((g) => [g.id, g.title, g.items.map((s) => s.name)])).toEqual([
      ['attention', 'Needs attention', ['a-part', 'b-sick']],
      ['running', 'Running', ['a-run', 'z-run']],
      ['stopped', 'Stopped', ['m-off']],
    ]);
    expect(groupStacks([stack('u', { status: 'unknown' })]).map((g) => g.title)).toEqual(['Unknown']);
    expect(groupStacks([])).toEqual([]);
  });
});

describe('rowPrimaryAction', () => {
  it.each([
    ['stopped', 'up', 'Up'],
    ['unavailable', 'up', 'Up'],
    ['unknown', 'up', 'Up'],
    ['running', 'restart', 'Restart'],
    ['partial', 'restart', 'Restart'],
  ] as const)('%s → %s', (status, action, label) => {
    expect(rowPrimaryAction(stack('a', { status }))).toEqual({ action, label });
  });
});

describe('filterByQuery', () => {
  it('matches name, project and path case-insensitively', () => {
    const items = [stack('Alpha', { project: 'web', path: '/srv/x/compose.yaml' }), stack('beta', { project: 'DB', path: '/opt/y/compose.yaml' })];
    expect(filterByQuery(items, 'ALPH').map((s) => s.name)).toEqual(['Alpha']);
    expect(filterByQuery(items, 'db').map((s) => s.name)).toEqual(['beta']);
    expect(filterByQuery(items, '/OPT').map((s) => s.name)).toEqual(['beta']);
    expect(filterByQuery(items, '')).toHaveLength(2);
  });
});

describe('headline', () => {
  const up = { docker: 'ok', connection: 'open' } as const;
  const alert = (over: Partial<Alert> = {}): Alert => ({ kind: 'exited', severity: 'danger', stack: 'a', service: 's', message: 'm', since: '2026-11-07T10:00:00Z', ...over });

  it('names the count of stacks needing attention', () => {
    expect(headline([bad('a'), stack('b')], [], up).title).toBe('1 stack needs attention');
    expect(headline([bad('a'), bad('b'), stack('c')], [], up).title).toBe('2 stacks need attention');
  });

  it('counts a stack that only an alert flags, and alerts with no stack as issues', () => {
    expect(headline([stack('a'), stack('b')], [alert({ stack: 'b' }), alert({ stack: 'b', service: 'x' })], up).title).toBe('1 stack needs attention');
    expect(headline([stack('a')], [alert({ stack: undefined, container: 'c', kind: 'unhealthy', severity: 'warning' })], up).title).toBe('1 issue needs attention');
    expect(headline([stack('a')], [alert({ severity: 'info', kind: 'something' })], up).title).toBe('All stacks are running');
  });

  it('never says healthy while alerts are unloaded, Docker is unknown or live updates are down', () => {
    const calm = [stack('a'), stack('b')];
    for (const h of [headline(calm, undefined, up), headline(calm, [], { docker: 'unknown', connection: 'open' }), headline(calm, [], { docker: 'ok', connection: 'reconnecting' })]) {
      expect(h.title).toBe('Health is not confirmed');
      expect(h.title + h.sub).not.toMatch(/healthy|all stacks are running|nothing needs/i);
    }
    expect(headline(calm, [], up).title).toBe('All stacks are running');
  });

  it('still reports stacks that need attention while alerts are unloaded', () => {
    expect(headline([bad('a')], undefined, up).title).toBe('1 stack needs attention');
  });

  it('Docker unavailable wins over every other state', () => {
    const h = headline([stack('a')], [], { docker: 'unavailable', connection: 'open' });
    expect(h.title).toBe('Docker is not responding');
    expect(h.sub).toMatch(/still running/);
  });

  it('unknown stack status is reported as unavailable, not healthy', () => {
    expect(headline([stack('a', { status: 'unknown' })], [], up).title).toBe('Stack status is unavailable');
  });

  it('the sub-line counts stacks and containers', () => {
    const host = { containers: { total: 26, running: 21, exited: 5, bad: 0 } } as HostSnapshot;
    expect(headline([bad('a'), stack('b')], [], up, host).sub).toBe('1 of 2 stacks fully running · 21 of 26 containers running');
  });
});

describe('fleetHeadline', () => {
  it('counts reachable servers and says what is hidden', () => {
    const ok = { status: 'reachable' };
    expect(fleetHeadline([ok, ok], [stack('a')])).toEqual({ title: 'All 2 servers are reachable', sub: '1 stack' });
    const h = fleetHeadline([ok, ok, { status: 'unreachable' }], [bad('a'), stack('b')]);
    expect(h.title).toBe('2 of 3 servers are reachable');
    expect(h.sub).toBe('2 stacks · 1 needs attention · stacks on servers that are not answering are hidden');
  });
});
