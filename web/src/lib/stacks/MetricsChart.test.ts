import { render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SeriesSet } from '../metrics/series';
import MetricsChart from './MetricsChart.svelte';

const T0 = Date.parse('2026-10-14T10:00:00Z');
const series: SeriesSet = new Map([
  ['api', [{ t: T0, cpu: 5, mem: 100, memLimit: 1000 }, { t: T0 + 5000, cpu: 12.34, mem: 2048, memLimit: 4096, netRx: 1536 }]],
  ['db', [{ t: T0 + 5000, cpu: 40, mem: 1024, memLimit: 4096 }]],
]);

function fakePlot() {
  const plot = { setData: vi.fn(), setSize: vi.fn(), destroy: vi.fn() };
  const createPlot = vi.fn(async () => plot);
  return { plot, createPlot };
}
const flush = () => new Promise((r) => setTimeout(r, 0));

afterEach(() => vi.unstubAllGlobals());

describe('MetricsChart', () => {
  it('passes ms-based data and one series per service to the plot', async () => {
    const { createPlot } = fakePlot();
    render(MetricsChart, { title: 'CPU (avg %)', series, metric: 'cpu', unit: 'percent', services: ['api', 'db'], createPlot });
    await flush();
    expect(createPlot).toHaveBeenCalledTimes(1);
    const [opts, data] = createPlot.mock.calls[0] as unknown as [{ ms: number; series: { label?: string }[] }, unknown[][]];
    expect(opts.ms).toBe(1);
    expect(opts.series.map((s) => s.label)).toEqual([undefined, 'api', 'db']);
    expect(data[0]).toEqual([T0, T0 + 5000]);
    expect(data[1]).toEqual([5, 12.34]);
    expect(data[2]).toEqual([null, 40]);
  });

  it('adds a dashed limit series for memory', async () => {
    const { createPlot } = fakePlot();
    render(MetricsChart, { title: 'Memory (avg)', series, metric: 'mem', unit: 'bytes', services: ['api', 'db'], memLimit: 4096, createPlot });
    await flush();
    const [opts, data] = createPlot.mock.calls[0] as unknown as [{ series: { label?: string; dash?: number[] }[] }, unknown[][]];
    const limit = opts.series.at(-1)!;
    expect(limit.label).toMatch(/limit/i);
    expect(limit.dash).toBeTruthy();
    expect(data.at(-1)).toEqual([4096, 4096]);
  });

  it('renders a table with the latest value per service and an aria-label summary', async () => {
    const { createPlot } = fakePlot();
    render(MetricsChart, { title: 'CPU (avg %)', series, metric: 'cpu', unit: 'percent', services: ['api', 'db'], createPlot });
    const table = screen.getByRole('table', { hidden: true });
    expect(table.textContent).toContain('api');
    expect(table.textContent).toContain('12.3%');
    expect(table.textContent).toContain('40.0%');
    const img = screen.getByRole('img');
    expect(img.getAttribute('aria-label')).toContain('api 12.3%');
    expect(img.getAttribute('aria-label')).toContain('db 40.0%');
  });

  it('formats rates and shows a dash when a service has no value', async () => {
    const { createPlot } = fakePlot();
    render(MetricsChart, { title: 'Network RX (total)', series, metric: 'netRx', unit: 'bytesPerSec', services: ['api', 'db'], createPlot });
    const table = screen.getByRole('table', { hidden: true });
    expect(table.textContent).toContain('1.5 KiB/s');
    expect(table.textContent).toContain('–');
  });

  it('updates data in place when the series change, resizes, and destroys on unmount', async () => {
    const { plot, createPlot } = fakePlot();
    let resizeCb: () => void = () => {};
    const disconnect = vi.fn();
    vi.stubGlobal(
      'ResizeObserver',
      class {
        constructor(cb: () => void) {
          resizeCb = cb;
        }
        observe() {}
        disconnect = disconnect;
      },
    );
    const view = render(MetricsChart, { title: 'CPU', series, metric: 'cpu', unit: 'percent', services: ['api', 'db'], createPlot });
    await flush();
    const next: SeriesSet = new Map([['api', [{ t: T0 + 10_000, cpu: 1, mem: 0, memLimit: 0 }]]]);
    await view.rerender({ series: next });
    await flush();
    expect(plot.setData).toHaveBeenCalled();
    expect((plot.setData.mock.calls.at(-1) as unknown as unknown[][][])[0][0]).toEqual([T0 + 10_000]);
    resizeCb();
    expect(plot.setSize).toHaveBeenCalled();
    view.unmount();
    expect(plot.destroy).toHaveBeenCalledTimes(1);
    expect(disconnect).toHaveBeenCalled();
  });

  it('destroys a plot that finished creating after unmount', async () => {
    const { plot } = fakePlot();
    let resolve!: (p: typeof plot) => void;
    const createPlot = vi.fn(() => new Promise<typeof plot>((r) => (resolve = r)));
    const view = render(MetricsChart, { title: 'CPU', series, metric: 'cpu', unit: 'percent', services: ['api'], createPlot });
    view.unmount();
    resolve(plot);
    await flush();
    expect(plot.destroy).toHaveBeenCalledTimes(1);
  });
});
