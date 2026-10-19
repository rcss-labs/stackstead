import { act, cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SeriesSet } from '$lib/metrics/series';
import { MetricsChart, type ChartSeries } from './MetricsChart';

const T0 = Date.parse('2026-10-14T10:00:00Z');
const data: SeriesSet = new Map([
  ['api', [{ t: T0, cpu: 5, mem: 100, memLimit: 1000 }, { t: T0 + 5000, cpu: 12.34, mem: 2048, memLimit: 4096, netRx: 1536 }]],
  ['db', [{ t: T0 + 5000, cpu: 40, mem: 1024, memLimit: 4096 }]],
]);
const cpuSeries: ChartSeries[] = [
  { label: 'api', service: 'api', metric: 'cpu' },
  { label: 'db', service: 'db', metric: 'cpu' },
];
const memSeries: ChartSeries[] = [
  { label: 'api', service: 'api', metric: 'mem' },
  { label: 'db', service: 'db', metric: 'mem' },
];
const all = ['api', 'db'];

function fakePlot() {
  const plot = { setData: vi.fn(), setSize: vi.fn(), destroy: vi.fn() };
  const createPlot = vi.fn(async () => plot);
  return { plot, createPlot };
}
const flush = () => act(() => new Promise<void>((r) => setTimeout(r, 0)));
type Opts = { ms: number; series: { label?: string; dash?: number[]; stroke?: string; value?: (u: unknown, v: number | null) => string }[]; legend: { show: boolean } };
const optsOf = (createPlot: ReturnType<typeof fakePlot>['createPlot'], call = 0) => (createPlot.mock.calls[call] as unknown as [Opts, unknown[][]])[0];
const dataOf = (createPlot: ReturnType<typeof fakePlot>['createPlot'], call = 0) => (createPlot.mock.calls[call] as unknown as [Opts, unknown[][]])[1];

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe('MetricsChart', () => {
  it('passes ms-based data and one series per entry to the plot', async () => {
    const { createPlot } = fakePlot();
    render(<MetricsChart title="CPU" series={cpuSeries} data={data} unit="percent" allServices={all} createPlot={createPlot} />);
    await flush();
    expect(createPlot).toHaveBeenCalledTimes(1);
    expect(optsOf(createPlot).ms).toBe(1);
    expect(optsOf(createPlot).series.map((s) => s.label)).toEqual(['Time', 'api', 'db']);
    const d = dataOf(createPlot);
    expect(d[0]).toEqual([T0, T0 + 5000]);
    expect(d[1]).toEqual([5, 12.34]);
    expect(d[2]).toEqual([null, 40]);
  });

  it('adds a dashed limit series for memory', async () => {
    const { createPlot } = fakePlot();
    render(<MetricsChart title="Memory" series={memSeries} data={data} unit="bytes" allServices={all} memLimit={4096} createPlot={createPlot} />);
    await flush();
    const limit = optsOf(createPlot).series.at(-1)!;
    expect(limit.label).toBe('Limit');
    expect(limit.dash).toBeTruthy();
    expect(dataOf(createPlot).at(-1)).toEqual([4096, 4096]);
  });

  it('draws per-service limit lines when limits differ', async () => {
    const { createPlot } = fakePlot();
    render(<MetricsChart title="Memory" series={memSeries} data={data} unit="bytes" allServices={all} limitPerService createPlot={createPlot} />);
    await flush();
    expect(optsOf(createPlot).series.map((x) => x.label)).toEqual(['Time', 'api', 'db', 'api limit', 'db limit']);
  });

  it('renders a table with the latest value per series and an aria-label summary', () => {
    const { createPlot } = fakePlot();
    render(<MetricsChart title="CPU" series={cpuSeries} data={data} unit="percent" allServices={all} createPlot={createPlot} />);
    const table = screen.getByRole('table', { hidden: true });
    expect(table.textContent).toContain('api');
    expect(table.textContent).toContain('12.3%');
    expect(table.textContent).toContain('40.0%');
    const label = screen.getByRole('img').getAttribute('aria-label')!;
    expect(label).toContain('CPU: api 12.3%');
    expect(label).toContain('db 40.0%');
  });

  it('formats rates and shows a dash when a series has no value', () => {
    const { createPlot } = fakePlot();
    const series: ChartSeries[] = [
      { label: 'api', service: 'api', metric: 'netRx' },
      { label: 'db', service: 'db', metric: 'netRx' },
    ];
    render(<MetricsChart title="Network" series={series} data={data} unit="bytesPerSec" allServices={all} createPlot={createPlot} />);
    const table = screen.getByRole('table', { hidden: true });
    expect(table.textContent).toContain('1.5 KiB/s');
    expect(table.textContent).toContain('–');
    expect(screen.getByRole('img').getAttribute('aria-label')).toContain('db no data');
  });

  it('updates data in place when the data change, resizes, and destroys on unmount', async () => {
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
    const view = render(<MetricsChart title="CPU" series={cpuSeries} data={data} unit="percent" allServices={all} createPlot={createPlot} />);
    await flush();
    const next: SeriesSet = new Map([['api', [{ t: T0 + 10_000, cpu: 1, mem: 0, memLimit: 0 }]]]);
    view.rerender(<MetricsChart title="CPU" series={cpuSeries} data={next} unit="percent" allServices={all} createPlot={createPlot} />);
    await flush();
    expect(createPlot).toHaveBeenCalledTimes(1);
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
    const view = render(<MetricsChart title="CPU" series={cpuSeries.slice(0, 1)} data={data} unit="percent" allServices={all} createPlot={createPlot} />);
    view.unmount();
    resolve(plot);
    await flush();
    expect(plot.destroy).toHaveBeenCalledTimes(1);
  });

  it('TestStructureChangeBeforeLoadDoesNotStackPlots', async () => {
    const resolvers: ((p: ReturnType<typeof fakePlot>['plot']) => void)[] = [];
    const plots = [fakePlot().plot, fakePlot().plot, fakePlot().plot];
    const createPlot = vi.fn(() => new Promise<(typeof plots)[0]>((r) => resolvers.push(r)));
    const ui = (series: ChartSeries[]) => <MetricsChart title="CPU" series={series} data={data} unit="percent" allServices={all} createPlot={createPlot} />;
    const view = render(ui([cpuSeries[0]!]));
    await flush();
    view.rerender(ui(cpuSeries));
    await flush();
    view.rerender(ui([cpuSeries[1]!]));
    await flush();
    expect(createPlot).toHaveBeenCalledTimes(3);
    await act(async () => resolvers.forEach((r, i) => r(plots[i]!)));
    await flush();
    expect(plots[0]!.destroy).toHaveBeenCalledTimes(1);
    expect(plots[1]!.destroy).toHaveBeenCalledTimes(1);
    expect(plots[2]!.destroy).not.toHaveBeenCalled();
    view.unmount();
    expect(plots[2]!.destroy).toHaveBeenCalledTimes(1);
  });

  it('formats hover values: time for x, unit-formatted values for series', async () => {
    const { createPlot } = fakePlot();
    render(<MetricsChart title="CPU" series={cpuSeries.slice(0, 1)} data={data} unit="percent" allServices={all} createPlot={createPlot} />);
    await flush();
    const opts = optsOf(createPlot);
    expect(opts.legend.show).toBe(true);
    expect(opts.series[1]!.value!(null, 12.34)).toBe('12.3%');
    expect(opts.series[1]!.value!(null, null)).toBe('–');
    expect(opts.series[0]!.value!(null, T0)).toBe(new Date(T0).toLocaleString());
  });

  it('shows "Chart unavailable" when the plot cannot be created and keeps the table', async () => {
    const createPlot = vi.fn(async () => {
      throw new Error('chunk failed');
    });
    render(<MetricsChart title="CPU" series={cpuSeries.slice(0, 1)} data={data} unit="percent" allServices={all} createPlot={createPlot} />);
    expect(await screen.findByText('Chart unavailable')).toBeTruthy();
    expect(screen.getByRole('table', { hidden: true }).textContent).toContain('12.3%');
  });

  it('series colours come from the tokens and a second metric of the same service takes the next slot', async () => {
    document.documentElement.style.setProperty('--series-1', '#123456');
    document.documentElement.style.setProperty('--series-2', '#654321');
    try {
      const { createPlot } = fakePlot();
      const series: ChartSeries[] = [
        { label: 'RX', service: 'api', metric: 'netRx' },
        { label: 'TX', service: 'api', metric: 'netTx' },
      ];
      render(<MetricsChart title="Network" series={series} data={data} unit="bytesPerSec" allServices={['api']} createPlot={createPlot} />);
      await flush();
      const opts = optsOf(createPlot);
      expect(opts.series[1]!.stroke).toBe('#123456');
      expect(opts.series[2]!.stroke).toBe('#654321');
    } finally {
      document.documentElement.style.removeProperty('--series-1');
      document.documentElement.style.removeProperty('--series-2');
    }
  });

  it('shows the stack total in the caption', () => {
    const { createPlot } = fakePlot();
    render(<MetricsChart title="CPU" series={cpuSeries} data={data} unit="percent" allServices={all} createPlot={createPlot} />);
    expect(screen.getByTestId('chart-total').textContent).toBe('52.3%');
  });

  it('rebuilds the plot with new colours when the colour scheme changes', async () => {
    const { plot, createPlot } = fakePlot();
    let listener: () => void = () => {};
    vi.stubGlobal('matchMedia', () => ({
      matches: false,
      addEventListener: (_: string, l: () => void) => (listener = l),
      removeEventListener: vi.fn(),
    }));
    render(<MetricsChart title="CPU" series={cpuSeries.slice(0, 1)} data={data} unit="percent" allServices={all} createPlot={createPlot} />);
    await flush();
    act(() => listener());
    await flush();
    expect(createPlot).toHaveBeenCalledTimes(2);
    expect(plot.destroy).toHaveBeenCalledTimes(1);
  });
});
