<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { api } from '../api/client';
  import Banner from '../components/Banner.svelte';
  import EmptyState from '../components/EmptyState.svelte';
  import Spinner from '../components/Spinner.svelte';
  import type { Range } from '../metrics/series';
  import type { openSSE } from '../sse/client';
  import MetricsChart, { type CreatePlot } from './MetricsChart.svelte';
  import { createMetricsController } from './metrics';

  let {
    stackName,
    metricsDegraded,
    createPlot,
    open,
  }: { stackName: string; metricsDegraded: boolean; createPlot?: CreatePlot; open?: typeof openSSE } = $props();

  // The panel is keyed by stack in the page; props are read once on purpose.
  /* svelte-ignore state_referenced_locally */
  const ctl = createMetricsController(stackName, {
    history: (q, signal) => api.metricsHistory(stackName, q, signal),
    open,
  });
  const { series, range, services, status, degraded } = ctl;

  let loaded = $state(false);
  onMount(() => {
    void ctl.reload().finally(() => (loaded = true));
  });
  onDestroy(() => ctl.close());

  const ranges: Range[] = ['1h', '6h', '24h', '72h'];

  const memLimit = $derived.by(() => {
    const limits = new Set<number>();
    for (const pts of $series.values()) {
      const last = pts[pts.length - 1];
      if (last && last.memLimit > 0) limits.add(last.memLimit);
    }
    return limits.size === 1 ? [...limits][0] : undefined;
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
    <label>Range
      <select bind:value={$range}>
        {#each ranges as r (r)}<option value={r}>{r}</option>{/each}
      </select>
    </label>
    <span class="status">Live: {$status}</span>
  </div>

  {#if !loaded}
    <Spinner label="Loading metrics" />
  {:else if $services.length === 0}
    <EmptyState title="No samples yet">The sampler records every 10 s.</EmptyState>
  {:else}
    <div class="grid">
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
  }
  .status {
    color: var(--muted);
    font-size: 0.85rem;
  }
  .grid {
    display: grid;
    gap: var(--space-3);
    grid-template-columns: repeat(auto-fit, minmax(min(100%, 28rem), 1fr));
  }
</style>
