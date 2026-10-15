import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installFakeFetch } from '../../vitest-setup';
import MetricsPanel from './MetricsPanel.svelte';

const fakePlot = async () => ({ setData() {}, setSize() {}, destroy() {} });

function history(points: unknown[]) {
  return installFakeFetch({ 'GET /api/stacks/web/metrics': { body: { points } } });
}

describe('MetricsPanel', () => {
  afterEach(() => vi.restoreAllMocks());

  it('shows the empty state when there are no samples', async () => {
    history([]);
    render(MetricsPanel, { stackName: 'web', metricsDegraded: false, createPlot: fakePlot, open: () => ({ close() {}, status: 'open' }) });
    expect(await screen.findByText(/No samples yet/)).toBeTruthy();
    expect(screen.getByText(/records every 10 s/)).toBeTruthy();
  });

  it('shows the degraded banner', async () => {
    history([]);
    render(MetricsPanel, { stackName: 'web', metricsDegraded: true, createPlot: fakePlot, open: () => ({ close() {}, status: 'open' }) });
    expect(
      await screen.findByText(/Metrics are not being recorded \(database write failures\); stack management is unaffected/),
    ).toBeTruthy();
  });

  it('renders labelled charts once history exists and refetches on range change', async () => {
    const fake = history([{ time: '2026-10-14T10:00:00Z', service: 'web', cpu: 3, mem: 10, memLimit: 100 }]);
    render(MetricsPanel, { stackName: 'web', metricsDegraded: false, createPlot: fakePlot, open: () => ({ close() {}, status: 'open' }) });
    expect(await screen.findByText('CPU (avg %)')).toBeTruthy();
    expect(screen.getByText('Network RX (total)')).toBeTruthy();
    expect(screen.getByText('Block write (total)')).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('button', { name: '24h' }));
    await waitFor(() => expect(fake.calls.some((c) => c.path.includes('step=2m'))).toBe(true));
  });

  it('range buttons switch range with aria-pressed', async () => {
    history([]);
    render(MetricsPanel, { stackName: 'web', metricsDegraded: false, createPlot: fakePlot, open: () => ({ close() {}, status: 'open' }) });
    const [h1, h6] = ['1h', '6h'].map((n) => screen.getByRole('button', { name: n }));
    expect(h1.getAttribute('aria-pressed')).toBe('true');
    expect(h6.getAttribute('aria-pressed')).toBe('false');
    expect(screen.getAllByRole('button').map((b) => b.textContent?.trim())).toEqual(['1h', '6h', '24h', '72h']);
    await userEvent.setup().click(h6);
    expect(h6.getAttribute('aria-pressed')).toBe('true');
    expect(h1.getAttribute('aria-pressed')).toBe('false');
  });

  it('shows sampling settings when known', async () => {
    history([]);
    render(MetricsPanel, {
      stackName: 'web',
      metricsDegraded: false,
      createPlot: fakePlot,
      open: () => ({ close() {}, status: 'open' }),
      metricsIntervalSeconds: 10,
      metricsRetentionSeconds: 259200,
    });
    expect(screen.getByText('sampled every 10 s · retention 72 h')).toBeTruthy();
    expect(screen.getAllByRole('status').some((el) => el.textContent?.includes('Live:'))).toBe(true);
  });

  it('omits the sampling line when the settings are unknown', async () => {
    history([]);
    render(MetricsPanel, { stackName: 'web', metricsDegraded: false, createPlot: fakePlot, open: () => ({ close() {}, status: 'open' }) });
    expect(screen.queryByText(/sampled every/)).toBeNull();
  });

  it('closes the live stream on unmount', async () => {
    history([]);
    const close = vi.fn();
    const view = render(MetricsPanel, { stackName: 'web', metricsDegraded: false, createPlot: fakePlot, open: () => ({ close, status: 'open' }) });
    await screen.findByText(/No samples yet/);
    view.unmount();
    expect(close).toHaveBeenCalled();
  });

  it('keeps the charts on screen while a range change reloads', async () => {
    let release: (() => void) | undefined;
    let calls = 0;
    installFakeFetch({
      'GET /api/stacks/web/metrics': async () => {
        calls++;
        if (calls > 1) await new Promise<void>((r) => (release = r));
        return { body: { points: [{ time: '2026-10-14T10:00:00Z', service: 'web', cpu: 3, mem: 10, memLimit: 100 }] } };
      },
    });
    render(MetricsPanel, { stackName: 'web', metricsDegraded: false, createPlot: fakePlot, open: () => ({ close() {}, status: 'open' }) });
    await screen.findByText('CPU (avg %)');
    await userEvent.setup().click(screen.getByRole('button', { name: '24h' }));
    await waitFor(() => expect(release).toBeDefined());
    expect(screen.getByText('CPU (avg %)')).toBeTruthy();
    expect(screen.queryByText('Loading metrics')).toBeNull();
    release?.();
  });
});
