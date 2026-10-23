import { describe, expect, it } from 'vitest';
import type { HostSnapshot, StackSummary } from '../api/types';
import {
  filterByQuery,
  groupStacks,
  panelLabel,
  rowPrimaryAction,
  statusFragments,
  statusSentence,
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

describe('panelLabel', () => {
  it('no stacks, all unknown, degraded singular and plural, all running, healthy with stopped', () => {
    expect(panelLabel([])).toEqual({ text: 'no stacks', tone: 'muted', pulse: false });
    expect(panelLabel([stack('a', { status: 'unknown' }), stack('b', { status: 'unknown' })])).toEqual({
      text: 'status unknown',
      tone: 'muted',
      pulse: false,
    });
    expect(panelLabel([bad('a'), stack('b')])).toEqual({ text: 'degraded · 1 stack', tone: 'danger', pulse: true });
    expect(panelLabel([bad('a'), bad('b')]).text).toBe('degraded · 2 stacks');
    expect(panelLabel([stack('a'), stack('b')])).toEqual({ text: 'all running · 2 stacks', tone: 'success', pulse: false });
    expect(panelLabel([stack('a'), stack('b', { status: 'stopped' }), stack('c', { status: 'stopped' })])).toEqual({
      text: 'healthy · 1 of 3 running',
      tone: 'success',
      pulse: false,
    });
  });
});

describe('statusSentence', () => {
  it('for 0, 1, 2 and 3+ attention stacks, all running, some stopped, all unknown', () => {
    expect(statusSentence([])).toBe('No stacks yet');
    expect(statusSentence([stack('a', { status: 'unknown' })])).toBe('Stack status is unavailable');
    expect(statusSentence([bad('b'), stack('a')])).toBe('b needs attention');
    expect(statusSentence([bad('b'), bad('a')])).toBe('a and b need attention');
    expect(statusSentence([bad('c'), bad('a'), bad('b')])).toBe('a and 2 more need attention');
    expect(statusSentence([bad('d'), bad('c'), bad('a'), bad('b')])).toBe('a and 3 more need attention');
    expect(statusSentence([stack('a'), stack('b')])).toBe('All stacks are running');
    expect(statusSentence([stack('a'), stack('b', { status: 'stopped' })])).toBe('Nothing needs attention');
  });
});

describe('statusFragments', () => {
  it('omit the containers fragment without a host snapshot and use singular for one stack', () => {
    expect(statusFragments([stack('a')], undefined)).toEqual(['1 of 1 stack fully running']);
    const host = { containers: { total: 19, running: 17, exited: 2, bad: 0 } } as HostSnapshot;
    expect(statusFragments([stack('a'), bad('b')], host)).toEqual(['1 of 2 stacks fully running', '17 of 19 containers up']);
    expect(statusFragments([stack('a')], { containers: null } as HostSnapshot)).toEqual(['1 of 1 stack fully running']);
  });
});

describe('statusFragments fully running', () => {
  it('does not count a running stack with an unhealthy service', () => {
    const unhealthy = stack('b', { health: { ...zero, unhealthy: 1 } });
    const items = [stack('a'), unhealthy, bad('c'), stack('d', { status: 'stopped' })];
    expect(statusFragments(items, undefined)).toEqual(['1 of 4 stacks fully running']);
  });
});

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
