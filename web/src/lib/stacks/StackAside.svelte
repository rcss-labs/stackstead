<script lang="ts">
  import type { Operation, StackDetail } from '$lib/api/types';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import { formatBytes, formatPercent, formatRate } from '$lib/format';
  import { link } from '$lib/router';
  import { operationStateLook } from './operation';
  import { totalsNow, type StackLiveMetrics } from './liveMetrics';
  import { serviceState } from './summary';

  let {
    detail,
    operations,
    live,
    opsHref,
    opHref,
  }: {
    detail: StackDetail;
    operations: Operation[];
    live: StackLiveMetrics;
    opsHref: string;
    opHref: (id: string) => string;
  } = $props();

  const latest = $derived(live.latest);
  const containers = $derived(
    detail.services.flatMap((svc) =>
      svc.containers.map((c) => ({ id: c.id, name: c.name, status: c.status, tone: serviceState({ ...svc, containers: [c] }).tone })),
    ),
  );
  const totals = $derived(totalsNow($latest));
  const net = $derived(
    totals.netRx === undefined && totals.netTx === undefined
      ? undefined
      : `${totals.netRx === undefined ? '—' : formatRate(totals.netRx)} · ${totals.netTx === undefined ? '—' : formatRate(totals.netTx)}`,
  );
  const recent = $derived([...operations].sort((a, b) => Date.parse(b.startedAt) - Date.parse(a.startedAt)).slice(0, 3));
</script>

<aside class="aside" aria-label="Stack summary">
  <section aria-labelledby="aside-containers">
    <h3 id="aside-containers">Containers</h3>
    {#if containers.length === 0}
      <p class="empty">No containers.</p>
    {:else}
      <ul class="containers">
        {#each containers as c (c.id)}
          <li><span class="dot {c.tone}" aria-hidden="true"></span><span class="mono">{c.name}</span><span class="muted status">{c.status}</span></li>
        {/each}
      </ul>
    {/if}
  </section>

  <section aria-labelledby="aside-resources">
    <h3 id="aside-resources">Resources · now</h3>
    <dl class="resources">
      <dt>CPU</dt>
      <dd class:dash={totals.cpu === undefined}>{totals.cpu === undefined ? '—' : formatPercent(totals.cpu)}</dd>
      <dt>Memory</dt>
      <dd class:dash={totals.mem === undefined}>{totals.mem === undefined ? '—' : formatBytes(totals.mem)}</dd>
      <dt>Net</dt>
      <dd class:dash={net === undefined}>{net ?? '—'}</dd>
    </dl>
  </section>

  <section aria-labelledby="aside-ops">
    <div class="row">
      <h3 id="aside-ops">Recent operations</h3>
      <a href={opsHref} use:link>All</a>
    </div>
    {#if recent.length === 0}
      <p class="empty">None yet.</p>
    {:else}
      <ul class="ops">
        {#each recent as o (o.id)}
          {@const look = operationStateLook(o.state)}
          <li>
            <span class="dot {look.tone}" aria-hidden="true"></span>
            <a href={opHref(o.id)} use:link>{o.action}</a>
            <span class="muted">{look.label}</span>
            <RelativeTime iso={o.startedAt} />
          </li>
        {/each}
      </ul>
    {/if}
  </section>
</aside>

<style>
  .aside { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
  section { border: 1px solid var(--border); border-radius: var(--radius-md); background: var(--surface); padding: 12px 14px; }
  h3 { margin: 0 0 10px; font-size: 11px; font-weight: 600; letter-spacing: 0.04em; text-transform: uppercase; color: var(--muted); }
  .row { display: flex; align-items: baseline; justify-content: space-between; }
  .row a { font-size: 12px; }
  ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; font-size: 12.5px; }
  li { display: flex; align-items: center; gap: 8px; min-width: 0; }
  .mono { font-family: var(--mono); overflow-wrap: anywhere; }
  .dot { width: 7px; height: 7px; border-radius: 50%; flex: none; background: var(--faint); }
  .dot.success { background: var(--success); }
  .dot.warning { background: var(--warning); }
  .dot.danger { background: var(--danger); }
  .resources { display: grid; grid-template-columns: 64px minmax(0, 1fr); gap: 6px 8px; margin: 0; font-size: 12.5px; }
  dt { color: var(--muted); }
  dd { margin: 0; font-family: var(--mono); }
  dd.dash { color: var(--faint); }
  .muted { color: var(--muted); }
  .status { margin-inline-start: auto; font-size: 11.5px; text-align: end; }
  .empty { margin: 0; color: var(--muted); font-size: 12.5px; }
</style>
