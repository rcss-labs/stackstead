<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { api } from '../api/client';
  import Banner from '../components/Banner.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Spinner from '../components/Spinner.svelte';
  import type { Range } from '../metrics/series';
  import type { openSSE } from '../sse/client';
  import MetricsChart, { type CreatePlot } from './MetricsChart.svelte';
  import type { StackLiveMetrics } from './liveMetrics';
  import { createMetricsController } from './metrics';

  let {
    stackName,
    metricsDegraded,
    createPlot,
    open,
    live,
    metricsIntervalSeconds,
    metricsRetentionSeconds,
  }: {
    stackName: string;
    metricsDegraded: boolean;
    createPlot?: CreatePlot;
    open?: typeof openSSE;
    /** The page's shared live source; without it the panel opens its own stream. */
    live?: StackLiveMetrics;
    metricsIntervalSeconds?: number;
    metricsRetentionSeconds?: number;
  } = $props();

  // The panel is keyed by stack in the page; props are read once on purpose.
  /* svelte-ignore state_referenced_locally */
  const ctl = createMetricsController(stackName, {
    history: (q, signal) => api.metricsHistory(stackName, q, signal),
    open,
    live,
  });
  const { series, range, services, status, degraded, loaded } = ctl;

  onMount(() => {
    void ctl.reload();
  });
  onDestroy(() => ctl.close());

  // The spinner is for the first load only; a range change keeps the old charts until the new data lands.
  let everLoaded = $state(false);
  $effect(() => {
    if ($loaded) everLoaded = true;
  });

  const ranges: Range[] = ['1h', '6h', '24h', '72h'];

  const memLimit = $derived.by(() => {
    // One shared line only when every service has a limit and they are all equal.
    const limits = new Set<number>();
    for (const service of $services) {
      const pts = $series.get(service) ?? [];
      limits.add(pts.length > 0 ? pts[pts.length - 1].memLimit : 0);
    }
    const [only] = limits;
    return limits.size === 1 && only > 0 ? only : undefined;
  });

  const charts = [
    { title: 'CPU (avg %)', metric: 'cpu', unit: 'percent' },
    { title: 'Memory (avg per container)', metric: 'mem', unit: 'bytes' },
    { title: 'Network RX (total)', metric: 'netRx', unit: 'bytesPerSec' },
    { title: 'Network TX (total)', metric: 'netTx', unit: 'bytesPerSec' },
    { title: 'Block read (total)', metric: 'blkRead', unit: 'bytesPerSec' },
    { title: 'Block write (total)', metric: 'blkWrite', unit: 'bytesPerSec' },
  ] as const;
</script>

<section class="panel" aria-label="Resource metrics">
  {#if metricsDegraded}
    <Banner kind="warning" title="Metrics are not being recorded (database write failures); stack management is unaffected" />
  {/if}
  {#if $degraded}
    <Banner kind="danger" title="Could not load metric history">Live samples may still appear below.</Banner>
  {/if}

  <div class="toolbar">
    <div class="ranges" role="group" aria-label="Range">
      {#each ranges as r (r)}
        <button type="button" aria-pressed={$range === r} onclick={() => ($range = r)}>{r}</button>
      {/each}
    </div>
    <span class="spacer"></span>
    {#if metricsIntervalSeconds !== undefined && metricsRetentionSeconds !== undefined}
      <span class="settings">sampled every {metricsIntervalSeconds} s · retention {metricsRetentionSeconds / 3600} h</span>
    {/if}
    <span class="status" role="status">Live: {$status}</span>
  </div>

  {#if !everLoaded}
    <Spinner label="Loading metrics" />
  {:else if $services.length === 0}
    <EmptyState title="No samples yet">The sampler records every 10 s.</EmptyState>
  {:else}
    <div class="grid" aria-busy={$loaded ? undefined : 'true'}>
      {#each charts as c (c.metric)}
        <MetricsChart
          title={c.title}
          series={$series}
          metric={c.metric}
          unit={c.unit}
          services={$services}
          memLimit={c.metric === 'mem' ? memLimit : undefined}
          {createPlot}
        />
      {/each}
    </div>
  {/if}
</section>

<style>
  .panel {
    display: grid;
    gap: var(--space-3);
  }
  .toolbar {
    display: flex;
    gap: var(--space-4);
    align-items: center;
    flex-wrap: wrap;
  }
  .spacer {
    flex: 1;
  }
  .ranges {
    display: inline-flex;
  }
  .ranges button {
    height: 26px;
    padding: 0 var(--space-3);
    font: 500 12px var(--mono);
    color: var(--muted);
    background: var(--raised);
    border: 1px solid var(--border-input);
    margin-left: -1px;
    cursor: pointer;
  }
  .ranges button:first-child {
    margin-left: 0;
    border-radius: var(--radius-sm) 0 0 var(--radius-sm);
  }
  .ranges button:last-child {
    border-radius: 0 var(--radius-sm) var(--radius-sm) 0;
  }
  .ranges button:hover {
    background: var(--hover);
  }
  .ranges button[aria-pressed='true'] {
    position: relative;
    color: var(--text);
    background: var(--accent-soft);
    border-color: var(--accent);
  }
  .settings,
  .status {
    color: var(--muted);
    font-size: 0.85rem;
  }
  .grid {
    display: grid;
    gap: 10px;
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr));
  }
</style>
