import { useEffect, useRef, useState } from 'react';
import type uPlot from 'uplot';
import { formatBytes, formatPercent, formatRate } from '$lib/format';
import type { SeriesPoint, SeriesSet } from '$lib/metrics/series';
import { COLOR_SLOTS, serviceColorIndex } from '$lib/stacks/metrics';
import styles from './MetricsChart.module.css';

export type MetricKey = 'cpu' | 'mem' | 'netRx' | 'netTx' | 'blkRead' | 'blkWrite';
export type Unit = 'percent' | 'bytes' | 'bytesPerSec';

export interface ChartSeries {
  label: string;
  service: string;
  metric: MetricKey;
}

export interface PlotHandle {
  setData(data: uPlot.AlignedData): void;
  setSize(size: { width: number; height: number }): void;
  destroy(): void;
}
export type CreatePlot = (opts: uPlot.Options, data: uPlot.AlignedData, el: HTMLElement) => Promise<PlotHandle>;

// uPlot (and its CSS) stay in their own chunk, loaded when the first chart mounts.
const defaultCreatePlot: CreatePlot = async (opts, data, el) => {
  const [{ default: UPlot }] = await Promise.all([import('uplot'), import('uplot/dist/uPlot.min.css')]);
  return new UPlot(opts, data, el);
};

export interface MetricsChartProps {
  title: string;
  series: ChartSeries[];
  data: SeriesSet;
  unit: Unit;
  /** Full service list used for colour assignment so filtering never repaints. */
  allServices: string[];
  /** One shared dashed limit line. */
  memLimit?: number;
  /** Otherwise: one dashed limit line per charted service that reports a limit. */
  limitPerService?: boolean;
  createPlot?: CreatePlot;
  height?: number;
}

interface LimitLine {
  label: string;
  service?: string;
  value?: number;
}

function latest(data: SeriesSet, service: string, key: keyof SeriesPoint): number | undefined {
  const pts = data.get(service) ?? [];
  for (let i = pts.length - 1; i >= 0; i--) {
    const v = pts[i]![key];
    if (v !== undefined) return v;
  }
  return undefined;
}

export function MetricsChart({ title, series, data, unit, allServices, memLimit, limitPerService, createPlot = defaultCreatePlot, height = 140 }: MetricsChartProps) {
  const fmt = (v: number) => (unit === 'percent' ? formatPercent(v) : unit === 'bytes' ? formatBytes(v) : formatRate(v));

  const rows = series.map((s) => ({ ...s, value: latest(data, s.service, s.metric) }));
  const values = rows.map((r) => r.value).filter((v): v is number => v !== undefined);
  const total = values.length === 0 ? undefined : values.reduce((a, b) => a + b, 0);
  const summary = `${title}: ` + rows.map((r) => `${r.label} ${r.value === undefined ? 'no data' : fmt(r.value)}`).join(', ');

  const limits: LimitLine[] = [];
  if (memLimit !== undefined && memLimit > 0) limits.push({ label: 'Limit', value: memLimit });
  else if (limitPerService) {
    for (const service of new Set(series.map((s) => s.service))) {
      if ((latest(data, service, 'memLimit') ?? 0) > 0) limits.push({ label: `${service} limit`, service });
    }
  }

  function buildData(): uPlot.AlignedData {
    const stamps = new Set<number>();
    for (const s of series) for (const p of data.get(s.service) ?? []) stamps.add(p.t);
    const xs = [...stamps].sort((a, b) => a - b);
    const column = (service: string, pick: (p: SeriesPoint) => number | undefined) => {
      const byT = new Map((data.get(service) ?? []).map((p) => [p.t, pick(p)]));
      return xs.map((t) => byT.get(t) ?? null);
    };
    const ys = series.map((s) => column(s.service, (p) => p[s.metric]));
    const extra = limits.map((l) =>
      l.value !== undefined ? xs.map(() => l.value as number) : column(l.service!, (p) => (p.memLimit > 0 ? p.memLimit : undefined)),
    );
    return [xs, ...ys, ...extra] as uPlot.AlignedData;
  }

  const hostRef = useRef<HTMLDivElement>(null);
  const [themeTick, setThemeTick] = useState(0);
  const structure = series.map((s) => s.label).join('\u0000') + '|' + limits.map((l) => l.label).join('\u0000') + '|' + themeTick;
  const [failedStructure, setFailedStructure] = useState<string | undefined>();
  const failed = failedStructure === structure;
  const plotRef = useRef<PlotHandle | undefined>(undefined);

  // The mount effect reads the newest props through this ref so it only re-runs on a structure change.
  const latestRef = useRef({ buildData, createPlot, height, unit, fmt, series, limits, allServices });
  useEffect(() => {
    latestRef.current = { buildData, createPlot, height, unit, fmt, series, limits, allServices };
  });

  // Series colours are read from computed style at build time, so rebuild when the scheme flips.
  useEffect(() => {
    const bump = () => setThemeTick((n) => n + 1);
    const mq = typeof matchMedia === 'function' ? matchMedia('(prefers-color-scheme: dark)') : undefined;
    mq?.addEventListener('change', bump);
    const mo = typeof MutationObserver !== 'undefined' ? new MutationObserver(bump) : undefined;
    mo?.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    return () => {
      mq?.removeEventListener('change', bump);
      mo?.disconnect();
    };
  }, []);

  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;
    const cur = latestRef.current;
    const cssColor = (name: string, fallback: string) => getComputedStyle(el).getPropertyValue(name).trim() || fallback;
    const muted = cssColor('--muted', '#5b6472');
    const grid = cssColor('--border', '#d5d9e0');
    const axis = { stroke: muted, grid: { stroke: grid, width: 1 }, ticks: { stroke: grid, width: 1 } };
    const fmtCursor = (_u: unknown, v: number | null) => (v == null ? '–' : cur.fmt(v));
    // A second series of the same service takes the next colour slot.
    const seen = new Map<string, number>();
    const slot = (service: string) => {
      const n = seen.get(service) ?? 0;
      seen.set(service, n + 1);
      return (serviceColorIndex(service, cur.allServices) + n) % COLOR_SLOTS;
    };
    const lineSeries = cur.series.map((s) => ({
      label: s.label,
      value: fmtCursor,
      stroke: cssColor(`--series-${slot(s.service) + 1}`, '#2a78d6'),
      width: 2,
      spanGaps: false,
      points: { show: false },
    }));
    const limitSeries = cur.limits.map((l) => ({
      label: l.label,
      value: fmtCursor,
      stroke: l.service ? cssColor(`--series-${serviceColorIndex(l.service, cur.allServices) + 1}`, muted) : muted,
      width: 1,
      dash: [6, 4],
      points: { show: false },
    }));
    const unitNow = cur.unit;
    const opts = {
      width: el.clientWidth || 600,
      height: cur.height,
      ms: 1,
      legend: { show: true, live: true },
      cursor: { points: { size: 8 } },
      scales: { y: { range: (_u: unknown, _min: number, max: number) => (unitNow === 'percent' ? [0, Math.max(100, max)] : [0, Math.max(max, 1) * 1.1]) } },
      series: [{ label: 'Time', value: (_u: unknown, v: number | null) => (v == null ? '–' : new Date(v).toLocaleString()) }, ...lineSeries, ...limitSeries],
      axes: [axis, { ...axis, size: 70, values: (_u: unknown, ticks: number[]) => ticks.map(cur.fmt) }],
    } as unknown as uPlot.Options;

    // Per-effect flag: a newer mount must not un-cancel an older, still-loading one.
    let cancelled = false;
    let mine: PlotHandle | undefined;
    cur.createPlot(opts, cur.buildData(), el).then(
      (p) => {
        if (cancelled) {
          p.destroy();
          return;
        }
        mine = plotRef.current = p;
        p.setData(latestRef.current.buildData()); // data may have changed while the plot was loading
      },
      () => {
        if (!cancelled) setFailedStructure(structure);
      },
    );
    const ro = typeof ResizeObserver !== 'undefined' ? new ResizeObserver(() => mine?.setSize({ width: el.clientWidth || 600, height: latestRef.current.height })) : undefined;
    ro?.observe(el);
    return () => {
      cancelled = true;
      ro?.disconnect();
      mine?.destroy();
      if (plotRef.current === mine) plotRef.current = undefined;
      mine = undefined;
    };
  }, [structure]);

  useEffect(() => {
    plotRef.current?.setData(buildData());
  });

  return (
    <figure className={styles.chart}>
      <figcaption className={styles.caption}>
        <span>{title}</span>
        {total !== undefined && (
          <span className={styles.total} data-testid="chart-total">
            {fmt(total)}
          </span>
        )}
      </figcaption>
      <div ref={hostRef} className={styles.plot} style={{ minHeight: height }} role="img" aria-label={summary} />
      {failed && (
        <p className={styles.unavailable} role="status">
          Chart unavailable
        </p>
      )}
      <details className={styles.details}>
        <summary>Show as table</summary>
        <table className={styles.table}>
          <caption className={styles.sr}>{title}, latest values</caption>
          <thead>
            <tr>
              <th scope="col">Series</th>
              <th scope="col">Latest</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.label}>
                <th scope="row">{r.label}</th>
                <td>{r.value === undefined ? '–' : fmt(r.value)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </figure>
  );
}
