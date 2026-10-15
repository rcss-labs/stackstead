<script lang="ts">
  import type { TopEntry } from '$lib/api/types';
  import { formatBytes, formatPercent } from '$lib/format';

  let { top }: { top: { cpu: TopEntry[]; mem: TopEntry[] } } = $props();

  let metric = $state<'cpu' | 'mem'>('cpu');
  const rows = $derived(top[metric].slice(0, 5));
  const max = $derived(Math.max(0, ...rows.map((r) => r.value)));
  const ratio = (v: number) => (max > 0 ? Math.round((v / max) * 100) : 0);
  const options = [
    { value: 'cpu', label: 'CPU' },
    { value: 'mem', label: 'Memory' },
  ] as const;
</script>

<section class="card">
  <h3>
    Top consumers
    <span class="segmented">
      {#each options as o (o.value)}
        <button type="button" aria-pressed={metric === o.value ? 'true' : 'false'} onclick={() => (metric = o.value)}>{o.label}</button>
      {/each}
    </span>
  </h3>
  {#if rows.length === 0}
    <p class="empty">No samples yet.</p>
  {:else}
    <ul>
      {#each rows as r (`${r.project}|${r.container}`)}
        <li>
          <div class="line">
            <span class="name mono" title={`${r.container} · ${r.project}`}>{r.container} · {r.project}</span>
            <span class="value mono">{metric === 'cpu' ? formatPercent(r.value) : formatBytes(r.value)}</span>
          </div>
          <div class="bar" aria-hidden="true"><span class={metric} style={`width:${ratio(r.value)}%`}></span></div>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); padding: 10px 12px; min-width: 0; }
  h3 { display: flex; align-items: center; justify-content: space-between; gap: 8px; margin: 0 0 8px; font-size: 13px; font-weight: 600; }
  .mono { font-family: var(--mono); }
  .empty { margin: 0; color: var(--muted); font-size: 12.5px; }
  .segmented { display: inline-flex; }
  .segmented button {
    height: 18px; padding: 0 8px; font: inherit; font-size: 11px; font-weight: 400; color: var(--text);
    background: var(--surface); border: 1px solid var(--border-strong); margin-left: -1px; cursor: pointer;
  }
  .segmented button:first-child { margin-left: 0; border-radius: var(--radius) 0 0 var(--radius); }
  .segmented button:last-child { border-radius: 0 var(--radius) var(--radius) 0; }
  .segmented button:hover { background: var(--hover); }
  .segmented button[aria-pressed='true'] { background: var(--accent-soft); color: var(--accent); border-color: var(--accent); position: relative; font-weight: 600; }
  ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  .line { display: flex; justify-content: space-between; gap: 8px; font-size: 12px; }
  .name { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .value { flex: none; }
  .bar { height: 4px; margin-top: 3px; background: var(--border); border-radius: 2px; overflow: hidden; }
  .bar > span { display: block; height: 100%; }
  .bar > span.cpu { background: var(--accent); }
  .bar > span.mem { background: var(--info); }
</style>
