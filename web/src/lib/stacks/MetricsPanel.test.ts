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
    await userEvent.setup().selectOptions(screen.getByLabelText('Range'), '24h');
    await waitFor(() => expect(fake.calls.some((c) => c.path.includes('step=2m'))).toBe(true));
  });
});
