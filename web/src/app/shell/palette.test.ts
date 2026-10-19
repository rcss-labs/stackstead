import { describe, expect, it } from 'vitest';
import type { StackStatus, StackSummary } from '$lib/api/types';
import { nextIndex, paletteEntries } from './palette';

const healthy = { compose: '2.29.0', docker: 'ok', events: 'connected' } as const;

function stack(name: string, status: StackStatus, running: number, serviceCount: number): StackSummary {
  return {
    name,
    path: `/srv/${name}/compose.yaml`,
    project: name,
    origin: 'managed',
    status,
    serviceCount,
    running,
    health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
    ports: [],
  };
}

const stacks = [stack('grafana', 'running', 3, 3), stack('home-assistant', 'partial', 2, 3), stack('nextcloud', 'stopped', 0, 2)];

function entries(overrides: Partial<Parameters<typeof paletteEntries>[0]> = {}) {
  return paletteEntries({ query: '', stacks, live: healthy, busyStacks: new Set(), effectiveTheme: 'dark', ...overrides });
}

describe('paletteEntries', () => {
  it('groups in order with pages first', () => {
    const list = entries();
    const groups = list.map((e) => e.group);
    expect([...new Set(groups)]).toEqual(['Go to', 'Stacks', 'Actions']);
    expect(list.filter((e) => e.group === 'Go to').map((e) => e.label)).toEqual([
      'Stacks',
      'Containers',
      'Networks',
      'Storage',
      'New stack',
      'Import',
      'Settings',
    ]);
    expect(list.find((e) => e.group === 'Go to')?.hint).toBe('Page');
    expect(list.filter((e) => e.group === 'Actions').map((e) => e.label)).toEqual(['New stack', 'Clean up storage', 'Toggle theme']);
  });

  it('stack entries show n/m · status and the LED tone', () => {
    const byLabel = Object.fromEntries(entries().filter((e) => e.group === 'Stacks').map((e) => [e.label, e]));
    expect(byLabel.grafana.hint).toBe('3/3 · running');
    expect(byLabel.grafana.led).toEqual({ tone: 'success' });
    expect(byLabel['home-assistant'].hint).toBe('2/3 · partial');
    expect(byLabel['home-assistant'].led).toEqual({ tone: 'warning', pulse: true });
    expect(byLabel.nextcloud.led).toEqual({ tone: 'faint' });
    expect(byLabel.grafana.mono).toBe(true);
    expect(byLabel.grafana.command).toEqual({ kind: 'navigate', to: '/stacks/grafana' });
  });

  it('per-stack lifecycle actions appear only with a query', () => {
    expect(entries().some((e) => e.label.startsWith('Restart '))).toBe(false);
    const labels = entries({ query: 'o' }).filter((e) => e.group === 'Actions').map((e) => e.label);
    expect(labels).toContain('Restart grafana');
    expect(labels).toContain('Pull grafana');
    expect(labels).not.toContain('Up grafana');
    expect(labels).toContain('Up nextcloud');
    expect(labels).not.toContain('Restart nextcloud');
    expect(labels).toContain('Restart home-assistant');
    expect(labels).toContain('Up home-assistant');
    const restart = entries({ query: 'restart grafana' }).find((e) => e.label === 'Restart grafana');
    expect(restart?.command).toEqual({ kind: 'act', stack: 'grafana', action: 'restart' });
  });

  it('filter is case-insensitive over label, group and hint', () => {
    expect(entries({ query: 'GRAF' }).filter((e) => e.group === 'Stacks').map((e) => e.label)).toEqual(['grafana']);
    expect(entries({ query: 'appearance' }).map((e) => e.label)).toEqual(['Toggle theme']);
    expect(entries({ query: '2/3 · partial' }).map((e) => e.label)).toEqual(['home-assistant']);
    expect(entries({ query: 'go to' }).every((e) => e.group === 'Go to')).toBe(true);
    expect(entries({ query: 'zzz' })).toEqual([]);
  });

  it('disabled reason follows lifecycleDisabledReason (compose unavailable, docker down, busy)', () => {
    const restart = (input: Partial<Parameters<typeof paletteEntries>[0]>) =>
      entries({ query: 'restart grafana', ...input }).find((e) => e.label === 'Restart grafana')!;
    expect(restart({}).disabledReason).toBeUndefined();
    expect(restart({ live: { ...healthy, compose: 'unavailable' } })).toMatchObject({
      disabledReason: 'docker compose plugin not found',
      hint: 'docker compose plugin not found',
    });
    expect(restart({ live: { ...healthy, docker: 'unavailable' } }).disabledReason).toBe('Docker is unreachable');
    expect(restart({ live: { ...healthy, events: 'disconnected' } }).disabledReason).toBe('Docker is unreachable');
    expect(restart({ busyStacks: new Set(['grafana']) }).disabledReason).toBe('an operation is already running');
  });
});

describe('nextIndex', () => {
  it('clamps at both ends', () => {
    expect(nextIndex(0, -1, 3)).toBe(0);
    expect(nextIndex(2, 1, 3)).toBe(2);
    expect(nextIndex(0, 1, 3)).toBe(1);
    expect(nextIndex(1, -1, 3)).toBe(0);
    expect(nextIndex(0, 1, 0)).toBe(0);
  });
});
