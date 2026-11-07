import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { afterEach, describe, expect, it } from 'vitest';
import type { HostSnapshot, StackSummary } from '$lib/api/types';
import { HostStats } from './HostStats';
import { StackTowers } from './StackTowers';
import { StatusPanel } from './StatusPanel';

const stack = (name: string, over: Partial<StackSummary> = {}): StackSummary => ({
  name,
  path: `/srv/stacks/${name}/compose.yaml`,
  project: `proj-${name}`,
  origin: 'managed',
  status: 'running',
  serviceCount: 3,
  running: 3,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
  libraryUpdates: 0,
  ...over,
});

const hostSnapshot = (): HostSnapshot => ({
  hostname: 'box',
  os: 'linux',
  source: 'proc',
  sampledAt: new Date().toISOString(),
  containers: { total: 10, running: 8, exited: 2, bad: 0 },
  cpu: { percent: 18, cores: 8 },
  memory: { used: 4 * 1024 ** 3, total: 16 * 1024 ** 3, swapUsed: 0, swapTotal: 0 },
  disk: { path: '/srv/stacks', free: 400 * 1024 ** 3, total: 1000 * 1024 ** 3 },
  network: { rxBytesPerSec: 2048, txBytesPerSec: 1024, interface: 'eth0' },
  diskIO: null,
  volumes: null,
  uptime: { seconds: 41 * 86400 + 6 * 3600, load1: 1.42, load5: 1.1, load15: 0.9 },
  restarts24h: null,
});

function Where() {
  const { pathname } = useLocation();
  return <output aria-label="location">{pathname}</output>;
}

const inRouter = (ui: React.ReactElement) =>
  render(
    <MemoryRouter>
      {ui}
      <Where />
    </MemoryRouter>,
  );

afterEach(cleanup);

describe('StatusPanel', () => {
  it('shows the headline it is given with its sub-line and the last event', () => {
    const items = [stack('alpha'), stack('beta')];
    inRouter(<StatusPanel items={items} headline={{ title: '1 stack needs attention', sub: '1 of 2 stacks fully running' }} host={undefined} history={[]} lastEventIso={new Date().toISOString()} showActions />);
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('1 stack needs attention');
    expect(screen.getByText(/1 of 2 stacks fully running/)).toBeTruthy();
    expect(screen.getByText(/last event/)).toBeTruthy();
  });

  it('hides the sub-line when it is empty and the actions when told to', () => {
    inRouter(<StatusPanel items={[]} headline={{ title: 'No stacks yet', sub: '' }} host={undefined} history={[]} showActions={false} />);
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('No stacks yet');
    expect(screen.queryByRole('link', { name: 'New stack' })).toBeNull();
  });

  it('shows a disk forecast line only when given days', () => {
    const { rerender } = inRouter(<StatusPanel items={[]} headline={{ title: 't', sub: '' }} host={hostSnapshot()} history={[]} showActions={false} forecastDays={41} />);
    expect(screen.getByText('About 41 days until the disk is full at the current rate.')).toBeTruthy();
    rerender(
      <MemoryRouter>
        <StatusPanel items={[]} headline={{ title: 't', sub: '' }} host={hostSnapshot()} history={[]} showActions={false} forecastDays={0.3} />
      </MemoryRouter>,
    );
    expect(screen.getByText('Less than a day until the disk is full at the current rate.')).toBeTruthy();
    rerender(
      <MemoryRouter>
        <StatusPanel items={[]} headline={{ title: 't', sub: '' }} host={hostSnapshot()} history={[]} showActions={false} forecastDays={null} />
      </MemoryRouter>,
    );
    expect(screen.queryByText(/until the disk is full/)).toBeNull();
  });
});

describe('StackTowers', () => {
  it('render one block per service with the stack name and open the stack', async () => {
    const partial = stack('alpha', { status: 'partial', running: 2, health: { healthy: 0, unhealthy: 1, starting: 0, none: 0, failed: 0 } });
    inRouter(<StackTowers items={[partial]} />);
    const tower = screen.getByRole('button', { name: 'alpha' });
    expect(tower.getAttribute('title')).toBe('alpha · partial');
    const blocks = Array.from(tower.querySelectorAll('[data-block]')).map((b) => b.getAttribute('data-block'));
    expect(blocks).toEqual(['up', 'unhealthy', 'down']);
    await userEvent.setup().click(tower);
    expect(screen.getByLabelText('location').textContent).toBe('/stacks/alpha');
  });

  it("a stopped stack's tower is outlined", () => {
    inRouter(<StackTowers items={[stack('idle', { status: 'stopped', running: 0 })]} />);
    const blocks = Array.from(screen.getByRole('button', { name: 'idle' }).querySelectorAll('[data-block]'));
    expect(blocks.map((b) => b.getAttribute('data-block'))).toEqual(['off', 'off', 'off']);
  });
});

describe('HostStats', () => {
  it('render six cells with values from the snapshot and the Containers cell links to /containers', () => {
    const { container } = inRouter(<HostStats host={hostSnapshot()} history={[]} />);
    expect(container.querySelectorAll('[data-stat]')).toHaveLength(6);
    for (const text of ['18.0%', '8 cores', '4.0 GiB', '8/10', '41d 6h']) expect(screen.getByText(text)).toBeTruthy();
    const link = screen.getByRole('link', { name: /containers/i });
    expect(link.getAttribute('href')).toBe('/containers');
    expect(within(link).getByText('8/10')).toBeTruthy();
  });

  it('render nothing before the first snapshot', () => {
    const { container } = inRouter(<HostStats host={undefined} history={[]} />);
    expect(container.querySelectorAll('[data-stat]')).toHaveLength(0);
  });
});
