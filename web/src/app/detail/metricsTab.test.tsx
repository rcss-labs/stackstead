import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SeriesPoint } from '$lib/metrics/series';
import { writable } from '$lib/store';
import type { StackLiveMetrics } from '$lib/stacks/liveMetrics';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { MetricsTab, type MetricsTabProps } from './MetricsTab';

function fakeLive() {
  const live: StackLiveMetrics = {
    status: writable<'connecting' | 'open' | 'closed'>('open') as StackLiveMetrics['status'],
    latest: writable(new Map<string, SeriesPoint>()),
    onPoints: vi.fn(() => () => {}),
    acquire: vi.fn(() => () => {}),
    close: vi.fn(),
  };
  return live;
}
const makePlot = () => ({ setData: vi.fn(), setSize: vi.fn(), destroy: vi.fn() });
const point = (service: string, over: Record<string, unknown> = {}) => ({
  time: '2026-10-14T10:00:00Z',
  service,
  cpu: 3,
  mem: 10,
  memLimit: 100,
  ...over,
});

let fake: FakeFetch;
const history = (points: unknown[]) => (fake = installFakeFetch({ 'GET /api/stacks/web/metrics': { body: { points } } }));

function setup(over: Partial<MetricsTabProps> = {}) {
  const live = fakeLive();
  const createPlot = vi.fn(async () => makePlot());
  const view = render(
    <MetricsTab stackName="web" service="web" live={live} metricsDegraded={false} allServices={['web']} createPlot={createPlot} {...over} />,
  );
  return { live, createPlot, view };
}

afterEach(() => {
  cleanup();
  fake?.restore();
});

describe('MetricsTab', () => {
  it('shows the empty state when there are no samples', async () => {
    history([]);
    setup();
    expect(await screen.findByText('No samples yet')).toBeTruthy();
    expect(screen.getByText(/records every 10 s/)).toBeTruthy();
  });

  it('shows the degraded banner', async () => {
    history([]);
    setup({ metricsDegraded: true });
    expect(
      await screen.findByText(/Metrics are not being recorded \(database write failures\); stack management is unaffected/),
    ).toBeTruthy();
  });

  it('renders the four charts once history exists and refetches on range change', async () => {
    history([point('web')]);
    setup();
    for (const title of ['CPU · 1h', 'Memory · 1h', 'Network · 1h', 'Disk I/O · 1h']) expect(await screen.findByText(title)).toBeTruthy();
    expect(screen.getAllByRole('figure')).toHaveLength(4);
    await userEvent.setup().click(screen.getByRole('button', { name: '24h' }));
    await waitFor(() => expect(fake.calls.some((c) => c.path.includes('step=2m'))).toBe(true));
    expect(await screen.findByText('CPU · 24h')).toBeTruthy();
  });

  it('range buttons switch range with aria-pressed', async () => {
    history([]);
    setup();
    const [h1, h6] = ['1h', '6h'].map((n) => screen.getByRole('button', { name: n }));
    expect(h1!.getAttribute('aria-pressed')).toBe('true');
    expect(h6!.getAttribute('aria-pressed')).toBe('false');
    expect(screen.getAllByRole('button').map((b) => b.textContent?.trim())).toEqual(['1h', '6h', '24h', '72h']);
    await userEvent.setup().click(h6!);
    expect(h6!.getAttribute('aria-pressed')).toBe('true');
    expect(h1!.getAttribute('aria-pressed')).toBe('false');
  });

  it('shows sampling settings when known', async () => {
    history([]);
    setup({ metricsIntervalSeconds: 10, metricsRetentionSeconds: 259200 });
    expect(screen.getByText('sampled every 10 s · retention 72 h')).toBeTruthy();
    expect(screen.getAllByRole('status').some((el) => el.textContent?.includes('Live: open'))).toBe(true);
  });

  it('omits the sampling line when the settings are unknown', () => {
    history([]);
    setup();
    expect(screen.queryByText(/sampled every/)).toBeNull();
  });

  it('closes the controller on unmount without closing the shared live stream', async () => {
    history([]);
    const release = vi.fn();
    const live = fakeLive();
    live.acquire = vi.fn(() => release);
    const view = render(<MetricsTab stackName="web" service="web" live={live} metricsDegraded={false} allServices={['web']} />);
    await screen.findByText('No samples yet');
    expect(release).not.toHaveBeenCalled();
    view.unmount();
    expect(release.mock.calls.length).toBeGreaterThan(0); // the controller let go of its hold
    expect(live.close).not.toHaveBeenCalled();
  });

  it('keeps the charts on screen while a range change reloads', async () => {
    let release: (() => void) | undefined;
    let calls = 0;
    fake = installFakeFetch({
      'GET /api/stacks/web/metrics': async () => {
        calls++;
        if (calls > 1) await new Promise<void>((r) => (release = r));
        return { body: { points: [point('web')] } };
      },
    });
    setup();
    await screen.findByText('CPU · 1h');
    await userEvent.setup().click(screen.getByRole('button', { name: '24h' }));
    await waitFor(() => expect(release).toBeDefined());
    expect(screen.getByText('CPU · 24h')).toBeTruthy();
    expect(screen.getAllByRole('figure')).toHaveLength(4);
    expect(screen.queryByText('Loading metrics')).toBeNull();
    expect(within(screen.getAllByRole('figure')[0]!.parentElement!).getAllByRole('figure')).toHaveLength(4);
    expect(screen.getAllByRole('figure')[0]!.parentElement!.getAttribute('aria-busy')).toBe('true');
    release?.();
    await waitFor(() => expect(screen.getAllByRole('figure')[0]!.parentElement!.getAttribute('aria-busy')).toBeNull());
  });

  it("only the selected service's points are charted", async () => {
    history([point('web'), point('db')]);
    const { createPlot } = setup({ allServices: ['db', 'web'] });
    await screen.findByText('CPU · 1h');
    await waitFor(() => expect(createPlot).toHaveBeenCalledTimes(4));
    const labels = (createPlot.mock.calls as unknown as [{ series: { label?: string }[] }][]).flatMap(([o]) => o.series.map((s) => s.label));
    expect(labels).toContain('web');
    expect(labels).not.toContain('db');
  });
});
