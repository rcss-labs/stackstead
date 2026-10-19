<script lang="ts">
  import { api, ApiError } from '$lib/api/client';
  import type { Action, Operation, StackSummary } from '$lib/api/types';
  import Banner from '$lib/components/Banner.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import Button from '$lib/components/Button.svelte';
  import Sparkline from '$lib/components/Sparkline.svelte';
  import AlertsCard from '$lib/dashboard/AlertsCard.svelte';
  import { createDashboard } from '$lib/dashboard/controller';
  import EventsFeed from '$lib/dashboard/EventsFeed.svelte';
  import HostTile from '$lib/dashboard/HostTile.svelte';
  import { hostTiles } from '$lib/dashboard/tiles';
  import TopConsumersCard from '$lib/dashboard/TopConsumersCard.svelte';
  import { formatBytes, formatPercent, formatRate } from '$lib/format';
  import { link, navigate } from '$lib/router';
  import { ICONS } from '$lib/shell/icons';
  import { lifecycleDisabledReason } from '$lib/stacks/lifecycle';
  import { chipCounts, filterStacks, healthCell, portChips, type ChipFilter } from '$lib/stacks/summary';
  import { host as hostSnapshot, startHost } from '$lib/stores/host';
  import { changes, events, live, refreshStatus } from '$lib/stores/live';
  import { operationsRefresh, refreshRunningOps, refreshStacks, stacks } from '$lib/stores/stacks';
  import { toasts } from '$lib/stores/toasts';

  const dashboard = createDashboard({ alerts: api.alerts, summary: api.metricsSummary, changes });
  const { alerts: alertsStore, summary } = dashboard;
  $effect(() => {
    dashboard.start();
    const releaseHost = startHost({ host: api.host });
    return () => {
      dashboard.stop();
      releaseHost();
    };
  });
  const stackNames = $derived(Object.fromEntries($stacks.items.map((s) => [s.project, s.name])));
  const tiles = $derived($hostSnapshot ? hostTiles($hostSnapshot) : []);
  const netCell = (rx: number | null, tx: number | null) =>
    rx === null && tx === null ? '—' : `${rx === null ? '—' : formatRate(rx)} / ${tx === null ? '—' : formatRate(tx)}`;

  let query = $state('');
  let retrying = $state(false);
  async function retry() {
    retrying = true;
    try {
      await refreshStacks();
    } finally {
      retrying = false;
    }
  }
  let runningStacks = $state<Set<string>>(new Set());

  const sorted = $derived([...$stacks.items].sort((a, b) => a.name.localeCompare(b.name)));
  let chip = $state<ChipFilter>('all');
  const visible = $derived(filterStacks(sorted, chip, query));
  const counts = $derived(chipCounts($stacks.items));
  // Unavailable / Unknown chips only appear when they would match something.
  // A filter on a chip that no longer exists would show an empty list with no way back.
  $effect(() => {
    if ((chip === 'unavailable' || chip === 'unknown') && counts[chip] === 0) chip = 'all';
  });
  const chips = $derived<{ id: ChipFilter; label: string }[]>([
    { id: 'all', label: 'All' },
    { id: 'running', label: 'Running' },
    { id: 'partial', label: 'Partial' },
    { id: 'stopped', label: 'Stopped' },
    ...(counts.unavailable > 0 ? [{ id: 'unavailable' as const, label: 'Unavailable' }] : []),
    ...(counts.unknown > 0 ? [{ id: 'unknown' as const, label: 'Unknown' }] : []),
  ]);
  const degraded = $derived($stacks.items.length > 0 && $stacks.items.every((s) => s.status === 'unknown'));

  // Stacks with an inline action request in flight: stops a double click sending two requests.
  let acting = $state<Set<string>>(new Set());
  async function act(name: string, action: Action) {
    if (acting.has(name)) return;
    acting = new Set(acting).add(name);
    try {
      const o = await api.act(name, action);
      navigate(`/stacks/${encodeURIComponent(name)}?tab=operations&op=${encodeURIComponent(o.id)}`);
      refreshRunningOps();
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e);
      if (e instanceof ApiError && e.status === 503) void refreshStatus();
      toasts.push({ kind: e instanceof ApiError && e.status === 409 ? 'warning' : 'danger', message: `Could not run ${action}: ${message}` });
    } finally {
      const next = new Set(acting);
      next.delete(name);
      acting = next;
    }
  }
  const reasonFor = (s: StackSummary) =>
    lifecycleDisabledReason({ live: $live, status: s.status, busy: runningStacks.has(s.name) || acting.has(s.name) });
  const rowActions: { action: Action; label: string; icon: 'play' | 'stop' | 'restart' }[] = [
    { action: 'up', label: 'Up', icon: 'play' },
    { action: 'down', label: 'Down', icon: 'stop' },
    { action: 'restart', label: 'Restart', icon: 'restart' },
  ];

  // Running operations are rechecked when the list changes, when asked
  // (operationsRefresh), and with backoff while any are running: an operation
  // ending emits no container change, so polling is the only way the spinner clears.
  $effect(() => {
    void $stacks.items;
    void $operationsRefresh;
    if ($stacks.loading) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    let hadRunning = false;
    const schedule = () => {
      timer = setTimeout(check, Math.min(2000 * 2 ** attempt, 10000));
      attempt++;
    };
    const check = async () => {
      try {
        const r: Operation[] = await api.runningOperations();
        if (cancelled) return;
        runningStacks = new Set(r.map((o) => o.stack));
        hadRunning = r.length > 0;
        if (hadRunning) schedule();
      } catch {
        // The badge column still works without the spinner; keep trying only if we were tracking one.
        if (!cancelled && hadRunning) schedule();
      }
    };
    void check();
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  });
</script>

{#snippet icon(path: string, size: number, filled = false)}
  <svg width={size} height={size} viewBox="0 0 24 24" fill={filled ? 'currentColor' : 'none'} stroke={filled ? 'none' : 'currentColor'} stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={path}></path></svg>
{/snippet}

<section>
  <header class="head">
    <h1 id="stacks-heading">Stacks</h1>
    {#if $hostSnapshot}<span class="hostname">{$hostSnapshot.hostname}</span>{/if}
    <span class="spacer"></span>
    {#if $stacks.items.length > 0 || $stacks.error}
      <a class="lbtn secondary" href="/stacks/import" use:link>Import</a>
      <a class="lbtn primary" href="/stacks/new" use:link>{@render icon(ICONS.plus, 14)}New stack</a>
    {/if}
  </header>

  {#if $stacks.error}
    <Banner kind="danger" title="Could not load stacks">{#snippet children()}{$stacks.error}{/snippet}</Banner>
    {#if $stacks.items.length === 0}
      <p><Button variant="secondary" loading={retrying} onclick={() => void retry()}>{#snippet children()}Retry{/snippet}</Button></p>
    {/if}
  {/if}
  {#if degraded}
    <Banner kind="warning" title="Degraded">{#snippet children()}Container status is unavailable, so every stack shows as unknown.{/snippet}</Banner>
  {/if}

  {#if tiles.length > 0}
    <div class="tiles">
      {#each tiles as tile (tile.id)}<HostTile {tile} />{/each}
    </div>
  {/if}

  {#if $stacks.loading}
    <div class="card pad"><Spinner /></div>
  {:else if $stacks.items.length === 0 && $stacks.error}
    <!-- the banner above is the whole story -->
  {:else if $stacks.items.length === 0}
    <EmptyState title="No stacks yet" description="Create a stack or import an existing compose file.">
      {#snippet children()}
        <p class="actions">
          <a class="lbtn primary" href="/stacks/new" use:link>New stack</a>
          <a class="lbtn secondary" href="/stacks/import" use:link>Import</a>
        </p>
      {/snippet}
    </EmptyState>
  {:else}
    <div class="filters">
      <div class="chips">
        {#each chips as c (c.id)}
          <button type="button" class="chip" aria-pressed={chip === c.id ? 'true' : 'false'} onclick={() => (chip = c.id)}>
            {c.label} <span class="n">{counts[c.id]}</span>
          </button>
        {/each}
      </div>
      <label class="search">
        {@render icon(ICONS.search, 14)}
        <input type="search" aria-label="Search stacks" placeholder="Filter by name, project or path" bind:value={query} />
      </label>
    </div>
    <div class="card">
      <!-- explicit roles keep table semantics when the mobile layout sets display:block -->
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <table role="table" aria-labelledby="stacks-heading">
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <thead role="rowgroup">
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tr role="row">
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Stack</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Status</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Services</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Health</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Ports</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">CPU</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Memory</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Net RX / TX</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col"><span class="visually-hidden">Actions</span></th>
          </tr>
        </thead>
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <tbody role="rowgroup">
          {#each visible as s (s.name)}
            {@const health = healthCell(s.health)}
            {@const ports = portChips(s.ports)}
            {@const reason = reasonFor(s)}
            {@const usage = $summary?.stacks[s.project]}
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <tr role="row">
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Stack">
               <span class="stack-cell">
                <span class="sq {s.status}" aria-hidden="true"></span>
                <span class="stack-text">
                  <span class="name">
                    <a href={`/stacks/${encodeURIComponent(s.name)}`} title={`project ${s.project}`} use:link>{s.name}</a>
                    <span class="tag">{s.origin}</span>
                    {#if runningStacks.has(s.name)}<Spinner label={`Operation running on ${s.name}`} />{/if}
                  </span>
                  <span class="path" title={s.path}>{s.path}</span>
                </span>
               </span>
              </td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Status"><StatusBadge status={s.status} /></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Services" class="num">
                <span class="mono">{s.running}/{s.serviceCount}</span>
                <span class="cells" aria-hidden="true">
                  {#each Array.from({ length: s.serviceCount }, (_, i) => i) as i (i)}<span class="cell" class:on={i < s.running}></span>{/each}
                </span>
              </td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Health" class="num">
                {#if health.empty}
                  <span class="dash" title={health.title}>—</span>
                {:else}
                  <span class="health mono" title={health.title}>
                    <span class="good"><i class="dot"></i>{health.good}</span>
                    <span class="bad"><i class="dot"></i>{health.bad}</span>
                    {#if health.starting > 0}<span class="starting"><i class="dot"></i>{health.starting}</span>{/if}
                  </span>
                {/if}
              </td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Ports" class="num">
                {#if ports.shown.length === 0}
                  <span class="dash">—</span>
                {:else}
                  <span class="ports" title={ports.title}>
                    {#each ports.shown as p (p)}<span class="port mono">{p}</span>{/each}
                    {#if ports.more > 0}<span class="port more mono">+{ports.more}</span>{/if}
                  </span>
                {/if}
              </td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="CPU" class="num">
                <span class="spark">
                  <Sparkline values={usage?.cpu ?? []} tone="accent" />
                  <span class="spark-value mono cpu">{usage?.cpuNow != null ? formatPercent(usage.cpuNow) : '—'}</span>
                </span>
              </td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Memory" class="num">
                <span class="spark">
                  <Sparkline values={usage?.mem ?? []} tone="info" />
                  <span class="spark-value mono mem">{usage?.memNow != null ? formatBytes(usage.memNow) : '—'}</span>
                </span>
              </td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Net RX / TX" class="num">
                <span class="net mono">{usage ? netCell(usage.netRxBytesPerSec, usage.netTxBytesPerSec) : '—'}</span>
              </td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Actions" class="row-actions">
                <span class="btns">
                  {#each rowActions as a (a.action)}
                    <Button variant="ghost" size="sm" icon aria-label={`${a.label} ${s.name}`} disabledReason={reason} onclick={() => void act(s.name, a.action)}>
                      {#snippet children()}{@render icon(ICONS[a.icon], 14, a.icon !== 'restart')}{/snippet}
                    </Button>
                  {/each}
                </span>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if visible.length === 0}<p class="none">No stacks match the current filter.</p>{/if}
    </div>
  {/if}

  <div class="sections">
    <AlertsCard alerts={$alertsStore} />
    <TopConsumersCard top={$summary?.top} {stackNames} />
    <EventsFeed events={$events} connection={$live.connection} />
  </div>
</section>

<style>
  .head { display: flex; flex-wrap: wrap; align-items: center; gap: var(--space-3); margin-bottom: var(--space-4); }
  .hostname { color: var(--muted); font-size: 14px; font-family: var(--mono); }
  .spacer { flex: 1; }
  .tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 8px; margin-bottom: var(--space-4); }
  .sections { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 10px; align-items: start; margin-top: var(--space-4); }
  .spark { display: inline-flex; align-items: center; gap: var(--space-2); }
  .spark-value { text-align: right; font-size: 12px; }
  .spark-value.cpu { min-width: 38px; }
  .spark-value.mem { min-width: 48px; }
  .net { font-size: 11.5px; color: var(--muted); white-space: nowrap; }
  .lbtn {
    display: inline-flex; align-items: center; gap: var(--space-2); height: 30px; padding: 0 12px;
    border: 1px solid var(--border-strong); border-radius: var(--radius); font-size: 13px; text-decoration: none;
    color: var(--text); background: var(--surface);
  }
  .lbtn:hover { background: var(--hover); }
  .lbtn.primary { background: var(--accent); border-color: var(--accent); color: var(--accent-ink); font-weight: 600; }
  .lbtn.primary:hover { background: var(--accent); filter: brightness(1.08); }
  .actions { display: flex; gap: var(--space-3); }
  .filters { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--space-3); margin-bottom: var(--space-3); }
  .chips { display: flex; flex-wrap: wrap; gap: var(--space-2); }
  .chip {
    display: inline-flex; align-items: center; gap: 6px; height: 26px; padding: 0 10px; font: inherit; font-size: 12px; font-weight: 500;
    border: 1px solid var(--border); border-radius: var(--radius-pill); background: transparent; color: var(--muted); cursor: pointer;
  }
  .chip:hover { background: var(--hover); color: var(--text); }
  .chip[aria-pressed='true'] { background: var(--accent-soft); border-color: var(--accent); color: var(--accent); }
  .n { font-family: var(--mono); color: var(--muted); font-weight: 400; }
  .search { position: relative; display: inline-flex; align-items: center; color: var(--muted); }
  .search :global(svg) { position: absolute; left: 10px; pointer-events: none; }
  .search input {
    width: 260px; max-width: 100%; box-sizing: border-box; height: 32px; padding: 0 10px 0 32px;
    background: var(--bg); color: var(--text); border: 1px solid var(--border-input); border-radius: var(--radius); font: inherit;
  }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); overflow-x: auto; }
  .card.pad { padding: var(--space-4); }
  table { width: 100%; border-collapse: collapse; }
  th { text-align: left; font-size: 12px; font-weight: 500; color: var(--muted); padding: var(--space-2) 12px; border-bottom: 1px solid var(--border); }
  td { text-align: left; height: var(--row); padding: var(--pad) 12px; border-bottom: 1px solid var(--border); vertical-align: middle; }
  td.num { padding: var(--pad) 8px; }
  tbody tr:hover { background: var(--hover); }
  tbody tr:last-child td { border-bottom: 0; }
  .stack-cell { display: flex; flex-direction: row; align-items: center; gap: var(--space-3); }
  .stack-text { display: flex; flex-direction: column; min-width: 0; }
  .name { display: inline-flex; align-items: center; gap: var(--space-2); }
  .tag { font-size: 11px; color: var(--muted); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 0 5px; }
  .path { max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-family: var(--mono); font-size: 10.5px; color: var(--faint); }
  .name a { font-weight: 500; text-decoration: none; color: var(--text); }
  .name a:hover { color: var(--accent); }
  .services { display: inline-flex; }
  td.num .mono:not(.net) { font-size: 12px; }
  .cells { display: inline-flex; gap: 2px; margin-left: var(--space-2); vertical-align: middle; }
  .cell { width: 10px; height: 6px; border-radius: 1px; background: var(--border-strong); }
  .cell.on { background: var(--success); }
  .dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: currentColor; margin-right: 4px; }
  .sq { flex: none; width: 8px; height: 8px; border-radius: 2px; background: var(--faint); }
  .sq.running { background: var(--success); }
  .sq.partial { background: var(--warning); }
  .sq.unavailable { background: var(--danger); }
  .mono { font-family: var(--mono); }
  .dash { color: var(--faint); }
  .health { display: inline-flex; gap: var(--space-2); }
  .good { color: var(--success); }
  .bad { color: var(--danger); }
  .starting { color: var(--warning); }
  .ports { display: inline-flex; flex-wrap: wrap; gap: 4px; }
  .port { font-size: 10.5px; background: var(--raised); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 0 5px; }
  .port.more { color: var(--faint); background: transparent; border-color: transparent; }
  .row-actions { width: 1%; }
  .btns { display: inline-flex; gap: var(--space-1); }
  .btns :global(button:nth-of-type(1):hover) { color: var(--success); }
  .btns :global(button:nth-of-type(2):hover) { color: var(--danger); }
  .btns :global(button:nth-of-type(3):hover) { color: var(--text); }
  .none { color: var(--muted); padding: var(--space-3) 12px; margin: 0; }

  @media (max-width: 720px) {
    .tiles { grid-template-columns: repeat(auto-fit, minmax(140px, 1fr)); }
    table, tbody, tr, td { display: block; }
    thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
    tr { border-bottom: 1px solid var(--border); padding: var(--space-2); }
    td { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); height: auto; border-bottom: 0; padding: var(--space-1) var(--space-2); }
    td::before { content: attr(data-label); color: var(--muted); }
    td[data-label='Stack']::before, .row-actions::before { content: none; }
    .row-actions { width: auto; }
    .btns { flex-direction: row; }
    .path { max-width: 60vw; }
    .search, .search input { width: 100%; }
    .search { flex: 1 1 100%; }
  }
</style>
