<script lang="ts" module>
  import type uPlot from 'uplot';

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
</script>

<script lang="ts">
  import { untrack } from 'svelte';
  import { formatBytes, formatPercent, formatRate } from '../format';
  import { toUplotData, type SeriesPoint, type SeriesSet } from '../metrics/series';
  import { serviceColorIndex } from './metrics';

  type MetricKey = 'cpu' | 'mem' | 'netRx' | 'netTx' | 'blkRead' | 'blkWrite';
  type Unit = 'percent' | 'bytes' | 'bytesPerSec';

  let {
    title,
    series,
    metric,
    unit,
    services,
    memLimit,
    allServices,
    createPlot = defaultCreatePlot,
  }: {
    title: string;
    series: SeriesSet;
    metric: MetricKey;
    unit: Unit;
    services: string[];
    memLimit?: number;
    /** Full service list used for colour assignment so filtering never repaints (defaults to `services`). */
    allServices?: string[];
    createPlot?: CreatePlot;
  } = $props();

  const fmt = (v: number) => (unit === 'percent' ? formatPercent(v) : unit === 'bytes' ? formatBytes(v) : formatRate(v));

  function latest(service: string, key: keyof SeriesPoint): number | undefined {
    const pts = series.get(service) ?? [];
    for (let i = pts.length - 1; i >= 0; i--) {
      const v = pts[i][key];
      if (v !== undefined) return v;
    }
    return undefined;
  }

  const rows = $derived(services.map((s) => ({ service: s, value: latest(s, metric), limit: latest(s, 'memLimit') })));
  const summary = $derived(
    `${title}: ` + rows.map((r) => `${r.service} ${r.value === undefined ? 'no data' : fmt(r.value)}`).join(', '),
  );

  /** Limit lines for the memory chart: one shared line, else one per service that has a limit. */
  const limits = $derived.by(() => {
    if (metric !== 'mem') return [] as { label: string; service?: string; value?: number | undefined }[];
    if (memLimit !== undefined && memLimit > 0) return [{ label: 'Limit', service: undefined as string | undefined, value: memLimit as number | undefined }];
    return rows
      .filter((r) => (r.limit ?? 0) > 0)
      .map((r) => ({ label: `${r.service} limit`, service: r.service, value: undefined as number | undefined }));
  });

  function buildData(): uPlot.AlignedData {
    const base = toUplotData(series, metric, services);
    const xs = base[0];
    const extra = limits.map((l) => {
      if (l.value !== undefined) return xs.map(() => l.value as number);
      const byT = new Map((series.get(l.service!) ?? []).map((p) => [p.t, p.memLimit]));
      return xs.map((t) => {
        const v = byT.get(t);
        return v && v > 0 ? v : null;
      });
    });
    return [...base, ...extra] as uPlot.AlignedData;
  }

  let host: HTMLDivElement | undefined = $state();
  let plot: PlotHandle | undefined;
  let failed = $state(false);
  let themeTick = $state(0);

  const cssColor = (name: string, fallback: string) => {
    if (!host) return fallback;
    return getComputedStyle(host).getPropertyValue(name).trim() || fallback;
  };

  const fmtCursor = (_u: unknown, v: number | null) => (v == null ? '–' : fmt(v));

  function buildOptions(width: number): uPlot.Options {
    const muted = cssColor('--muted', '#5b6472');
    const grid = cssColor('--border', '#d5d9e0');
    const axis = { stroke: muted, grid: { stroke: grid, width: 1 }, ticks: { stroke: grid, width: 1 } };
    const lineSeries = services.map((s) => ({
      label: s,
      value: fmtCursor,
      stroke: cssColor(`--series-${serviceColorIndex(s, allServices ?? services) + 1}`, '#2a78d6'),
      width: 2,
      spanGaps: false,
      points: { show: false },
    }));
    const limitSeries = limits.map((l) => ({
      label: l.label,
      value: fmtCursor,
      stroke: l.service
        ? cssColor(`--series-${serviceColorIndex(l.service, allServices ?? services) + 1}`, muted)
        : muted,
      width: 1,
      dash: [6, 4],
      points: { show: false },
    }));
    return {
      width,
      height: 180,
      ms: 1,
      legend: { show: true, live: true },
      cursor: { points: { size: 8 } },
      scales: { y: { range: (_u, min, max) => (unit === 'percent' ? [0, Math.max(100, max)] : [0, Math.max(max, 1) * 1.1]) } },
      series: [{ label: 'Time', value: (_u: unknown, v: number | null) => (v == null ? '–' : new Date(v).toLocaleString()) }, ...lineSeries, ...limitSeries],
      axes: [axis, { ...axis, size: 70, values: (_u, ticks) => ticks.map(fmt) }],
    } as uPlot.Options;
  }

  // Structure (services / limit lines) changes rebuild the plot; plain data changes reuse it.
  const structure = $derived(services.join('\u0000') + '|' + limits.map((l) => l.label).join('\u0000'));

  $effect(() => {
    const el = host;
    void structure;
    void themeTick;
    if (!el) return;
    return untrack(() => mountPlot(el));
  });

  // Series colours are read from computed style at build time, so rebuild when the scheme flips.
  $effect(() => {
    const bump = () => themeTick++;
    const mq = typeof matchMedia === 'function' ? matchMedia('(prefers-color-scheme: dark)') : undefined;
    mq?.addEventListener('change', bump);
    const mo = typeof MutationObserver !== 'undefined' ? new MutationObserver(bump) : undefined;
    mo?.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    return () => {
      mq?.removeEventListener('change', bump);
      mo?.disconnect();
    };
  });

  function mountPlot(el: HTMLDivElement) {
    // Per-mount flag: a newer mount must not un-cancel an older, still-loading one.
    let cancelled = false;
    let mine: PlotHandle | undefined;
    let ro: ResizeObserver | undefined;
    failed = false;
    createPlot(buildOptions(el.clientWidth || 600), buildData(), el).then(
      (p) => {
        if (cancelled) {
          p.destroy();
          return;
        }
        mine = plot = p;
        p.setData(buildData()); // data may have changed while the plot was loading
      },
      () => {
        if (!cancelled) failed = true;
      },
    );
    if (typeof ResizeObserver !== 'undefined') {
      ro = new ResizeObserver(() => mine?.setSize({ width: el.clientWidth || 600, height: 180 }));
      ro.observe(el);
    }
    return () => {
      cancelled = true;
      ro?.disconnect();
      mine?.destroy();
      if (plot === mine) plot = undefined;
      mine = undefined;
    };
  }

  $effect(() => {
    const data = buildData();
    plot?.setData(data);
  });
</script>

<figure class="chart">
  <figcaption>{title}</figcaption>
  <div bind:this={host} class="plot" role="img" aria-label={summary}></div>
  {#if failed}<p class="unavailable" role="status">Chart unavailable</p>{/if}
  <details>
    <summary>Show as table</summary>
    <table>
      <caption class="sr">{title}, latest values</caption>
      <thead><tr><th scope="col">Service</th><th scope="col">Latest</th></tr></thead>
      <tbody>
        {#each rows as r (r.service)}
          <tr><th scope="row">{r.service}</th><td>{r.value === undefined ? '–' : fmt(r.value)}</td></tr>
        {/each}
      </tbody>
    </table>
  </details>
</figure>

<style>
  .chart {
    --series-1: #2a78d6;
    --series-2: #eb6834;
    --series-3: #1baf7a;
    --series-4: #eda100;
    --series-5: #e87ba4;
    --series-6: #008300;
    --series-7: #4a3aa7;
    --series-8: #e34948;
    margin: 0;
    padding: var(--space-3);
    background: var(--raised);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
  }
  @media (prefers-color-scheme: dark) {
    :global(:root:not([data-theme='light'])) .chart {
      --series-1: #3987e5;
      --series-2: #d95926;
      --series-3: #199e70;
      --series-4: #c98500;
      --series-5: #d55181;
      --series-7: #9085e9;
      --series-8: #e66767;
    }
  }
  :global(:root[data-theme='dark']) .chart {
    --series-1: #3987e5;
    --series-2: #d95926;
    --series-3: #199e70;
    --series-4: #c98500;
    --series-5: #d55181;
    --series-7: #9085e9;
    --series-8: #e66767;
  }
  figcaption {
    font-weight: 600;
    margin-bottom: var(--space-2);
  }
  .plot {
    width: 100%;
    min-height: 180px;
  }
  .unavailable {
    color: var(--danger);
    margin: var(--space-2) 0 0;
  }
  .plot :global(.u-legend) {
    color: var(--muted);
    font-size: 0.85rem;
  }
  details {
    margin-top: var(--space-2);
    font-size: 0.85rem;
  }
  table {
    border-collapse: collapse;
    margin-top: var(--space-2);
  }
  th,
  td {
    text-align: left;
    padding: 2px var(--space-3) 2px 0;
  }
  .sr {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
  }
</style>
