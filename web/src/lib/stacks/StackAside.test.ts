import { render, screen, within } from '@testing-library/svelte';
import { writable } from 'svelte/store';
import { describe, expect, it } from 'vitest';
import type { ContainerInfo, Operation, StackDetail } from '$lib/api/types';
import type { SeriesPoint } from '$lib/metrics/series';
import type { StackLiveMetrics } from './liveMetrics';
import StackAside from './StackAside.svelte';

const ctr = (over: Partial<ContainerInfo>): ContainerInfo => ({
  id: 'c',
  name: 'x',
  service: 's',
  state: 'running',
  status: 'Up',
  health: 'none',
  ...over,
});
const detail = (): StackDetail => ({
  name: 'web',
  path: '/srv/web/compose.yaml',
  project: 'web',
  origin: 'managed',
  status: 'running',
  serviceCount: 2,
  running: 2,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
  hash: 'h',
  services: [
    { name: 'app', config: { limits: {} }, readOnly: [], containers: [ctr({ id: '1', name: 'web-app-1', service: 'app' })] },
    {
      name: 'db',
      config: { limits: {} },
      readOnly: [],
      containers: [ctr({ id: '2', name: 'web-db-1', service: 'db', health: 'unhealthy' })],
    },
  ],
});
const op = (id: string, startedAt: string, action = 'up'): Operation => ({ id, stack: 'web', action, state: 'succeeded', startedAt });

function fakeLive() {
  const latest = writable(new Map<string, SeriesPoint>());
  const live = {
    status: writable('open'),
    latest,
    onPoints: () => () => {},
    acquire: () => () => {},
    close: () => {},
  } as unknown as StackLiveMetrics;
  return { live, latest };
}
const point = (over: Partial<SeriesPoint>) => ({ t: 0, cpu: 0, mem: 0, memLimit: 0, ...over }) as SeriesPoint;

describe('StackAside', () => {
  it('lists containers of all services', () => {
    render(StackAside, { detail: detail(), operations: [], live: fakeLive().live, opsHref: '/stacks/web?tab=operations', opHref: (id: string) => `/stacks/web?tab=operations&op=${id}` });
    const section = screen.getByRole('region', { name: 'Containers' });
    expect(within(section).getByText('web-app-1')).toBeTruthy();
    expect(within(section).getByText('web-db-1')).toBeTruthy();
  });

  it('says so when there are no containers', () => {
    const d = detail();
    d.services = d.services.map((s) => ({ ...s, containers: [] }));
    render(StackAside, { detail: d, operations: [], live: fakeLive().live, opsHref: '/x', opHref: (id: string) => `/x&op=${id}` });
    expect(screen.getByText('No containers.')).toBeTruthy();
  });

  it('resources show dash until samples then totals', async () => {
    const { live, latest } = fakeLive();
    render(StackAside, { detail: detail(), operations: [], live, opsHref: '/x', opHref: (id: string) => `/x&op=${id}` });
    const section = screen.getByRole('region', { name: 'Resources · now' });
    expect(within(section).getAllByText('—')).toHaveLength(3);
    latest.set(
      new Map([
        ['app', point({ cpu: 10, mem: 1024 * 1024, netRx: 1024, netTx: 2048 })],
        ['db', point({ cpu: 5.5, mem: 1024 * 1024, netRx: 1024, netTx: 0 })],
      ]),
    );
    expect(await within(section).findByText('15.5%')).toBeTruthy();
    expect(within(section).getByText('2.0 MiB')).toBeTruthy();
    expect(within(section).queryByText('—')).toBeNull();
  });

  it('recent operations shows three newest and links to all', () => {
    const ops = [
      op('a', '2026-10-14T10:00:00Z', 'up'),
      op('b', '2026-10-14T11:00:00Z', 'down'),
      op('c', '2026-10-14T12:00:00Z', 'pull'),
      op('d', '2026-10-14T13:00:00Z', 'build'),
    ];
    render(StackAside, { detail: detail(), operations: ops, live: fakeLive().live, opsHref: '/stacks/web?tab=operations', opHref: (id: string) => `/stacks/web?tab=operations&op=${id}` });
    const section = screen.getByRole('region', { name: 'Recent operations' });
    expect(within(section).getAllByRole('listitem').map((li) => li.textContent?.match(/build|pull|down|up/)?.[0])).toEqual(['build', 'pull', 'down']);
    expect(within(section).getByRole('link', { name: 'build' }).getAttribute('href')).toBe('/stacks/web?tab=operations&op=d');
    expect(within(section).getByRole('link', { name: 'All' }).getAttribute('href')).toBe('/stacks/web?tab=operations');
  });

  it('shows None yet. without operations', () => {
    render(StackAside, { detail: detail(), operations: [], live: fakeLive().live, opsHref: '/x', opHref: (id: string) => `/x&op=${id}` });
    expect(screen.getByText('None yet.')).toBeTruthy();
  });
});
