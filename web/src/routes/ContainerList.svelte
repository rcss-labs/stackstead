<script lang="ts">
  import { api } from '$lib/api/client';
  import type { ContainerRow } from '$lib/api/types';
  import Banner from '$lib/components/Banner.svelte';
  import Button from '$lib/components/Button.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import ContainerActions from '$lib/containers/ContainerActions.svelte';
  import { chipCounts, createContainerList, filterContainers, isProblem, sortContainers, type ContainerChip } from '$lib/containers/list';
  import { formatBytes, formatPercent } from '$lib/format';
  import { link } from '$lib/router';
  import { ICONS } from '$lib/shell/icons';
  import { portChips } from '$lib/stacks/summary';
  import { changes } from '$lib/stores/live';

  const list = createContainerList({ list: api.containers, changes });
  const { rows, error } = list;
  $effect(() => {
    list.start();
    return () => list.stop();
  });

  let chip = $state<ContainerChip>('all');
  let query = $state('');
  const all = $derived($rows ?? []);
  const counts = $derived(chipCounts(all));
  const visible = $derived(filterContainers(sortContainers(all), chip, query));
  const chips: { id: ContainerChip; label: string }[] = [
    { id: 'all', label: 'All' },
    { id: 'running', label: 'Running' },
    { id: 'stopped', label: 'Stopped' },
    { id: 'problems', label: 'Problems' },
    { id: 'unmanaged', label: 'Unmanaged' },
  ];

  // Status dot colour is redundant with the state badge and health text beside it.
  function tone(c: ContainerRow): 'danger' | 'warning' | 'success' | 'muted' {
    if (isProblem(c)) return 'danger';
    if (c.health === 'starting' || c.state === 'paused') return 'warning';
    return c.state === 'running' ? 'success' : 'muted';
  }
</script>

<section>
  <header class="head">
    <h1 id="containers-heading">Containers</h1>
    {#if $rows}<span class="counts">{counts.all} · {counts.running} running</span>{/if}
  </header>

  {#if $error && !$rows}
    <Banner kind="danger" title="Could not load containers">{#snippet children()}{$error}{/snippet}</Banner>
    <p><Button variant="secondary" onclick={() => list.refresh()}>{#snippet children()}Retry{/snippet}</Button></p>
  {:else if !$rows}
    <div class="card pad"><Spinner label="Loading containers" /></div>
  {:else if all.length === 0}
    <EmptyState title="No containers on this host." description="Containers appear here as soon as Docker creates them." />
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
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={ICONS.search}></path></svg>
        <input type="search" aria-label="Search containers" placeholder="Filter by name, image, stack or port" bind:value={query} />
      </label>
    </div>
    <div class="card">
      <!-- explicit roles keep table semantics when the mobile layout sets display:block -->
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <table role="table" aria-labelledby="containers-heading">
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <thead role="rowgroup">
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tr role="row">
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Container</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Stack</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Image</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">State</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Status</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Ports</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">CPU</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Memory</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col">Restarts 24h</th>
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <th role="columnheader" scope="col"><span class="visually-hidden">Actions</span></th>
          </tr>
        </thead>
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <tbody role="rowgroup">
          {#each visible as c (c.id)}
            {@const ports = portChips(c.ports, 2)}
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <tr role="row">
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Container">
                <span class="name-cell">
                  <span class="sq {tone(c)}" aria-hidden="true"></span>
                  <span class="name-text">
                    <a class="name mono" href={`/containers/${encodeURIComponent(c.name)}`} use:link>{c.name}</a>
                    <span class="id">{c.id}</span>
                  </span>
                </span>
              </td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Stack">
                {#if c.managed === 'stack'}
                  <a href={`/stacks/${encodeURIComponent(c.stack)}`} use:link>{c.stack}</a>
                {:else if c.managed === 'compose'}
                  <span class="project">{c.project}<span class="tag">unregistered</span></span>
                {:else}
                  <span class="dash">—</span>
                {/if}
              </td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Image"><span class="image mono" title={c.image}>{c.image}</span></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="State">
                <span class="state">
                  <StatusBadge status={c.state as 'running'} />
                  {#if c.health !== 'none'}<span class="health {c.health}"><i class="dot"></i>{c.health}</span>{/if}
                </span>
              </td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Status"><span class="status">{c.status}</span></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Ports">
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
              <td role="cell" data-label="CPU"><span class="mono num">{c.cpu !== null ? formatPercent(c.cpu) : '—'}</span></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Memory"><span class="mono num">{c.mem !== null ? formatBytes(c.mem) : '—'}</span></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Restarts 24h"><span class="mono num" class:warn={c.restarts24h >= 3}>{c.restarts24h}</span></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Actions" class="row-actions">
                <span class="actions">
                  <a class="logs" href={`/containers/${encodeURIComponent(c.name)}?tab=logs`} use:link>Logs</a>
                  <ContainerActions row={c} onchanged={() => list.refresh()} />
                </span>
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
      {#if visible.length === 0}<p class="none">No containers match the current filter.</p>{/if}
    </div>
  {/if}
</section>

<style>
  .head { display: flex; flex-wrap: wrap; align-items: baseline; gap: var(--space-3); margin-bottom: var(--space-4); }
  .counts { color: var(--muted); font-size: 14px; font-family: var(--mono); }
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
  th { text-align: left; font-size: 12px; font-weight: 500; color: var(--muted); padding: var(--space-2) 12px; border-bottom: 1px solid var(--border); white-space: nowrap; }
  td { text-align: left; height: var(--row); padding: var(--pad) 12px; border-bottom: 1px solid var(--border); vertical-align: middle; }
  tbody tr:hover { background: var(--hover); }
  tbody tr:last-child td { border-bottom: 0; }
  .name-cell { display: flex; align-items: center; gap: var(--space-3); }
  .name-text { display: flex; flex-direction: column; min-width: 0; }
  .name { font-weight: 500; text-decoration: none; color: var(--text); font-size: 13px; }
  .name:hover { color: var(--accent); }
  .id { font-family: var(--mono); font-size: 10.5px; color: var(--faint); }
  .sq { flex: none; width: 8px; height: 8px; border-radius: 2px; background: var(--faint); }
  .sq.success { background: var(--success); }
  .sq.warning { background: var(--warning); }
  .sq.danger { background: var(--danger); }
  .mono { font-family: var(--mono); }
  .num { font-size: 12px; }
  .warn { color: var(--warning); font-weight: 600; }
  .dash { color: var(--faint); }
  .project { display: inline-flex; align-items: center; gap: var(--space-2); }
  .tag { font-size: 11px; color: var(--muted); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 0 5px; }
  .image { display: inline-block; max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; vertical-align: middle; font-size: 12px; }
  .state { display: inline-flex; align-items: center; gap: var(--space-2); }
  .health { display: inline-flex; align-items: center; font-size: 11px; color: var(--muted); }
  .health.healthy { color: var(--success); }
  .health.unhealthy { color: var(--danger); }
  .health.starting { color: var(--warning); }
  .dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: currentColor; margin-right: 4px; }
  .status { color: var(--muted); font-size: 12px; white-space: nowrap; }
  .ports { display: inline-flex; flex-wrap: wrap; gap: 4px; }
  .port { font-size: 10.5px; background: var(--raised); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 0 5px; }
  .port.more { color: var(--faint); background: transparent; border-color: transparent; }
  .row-actions { width: 1%; }
  .actions { display: inline-flex; align-items: center; gap: var(--space-3); white-space: nowrap; }
  .logs { font-size: 12px; color: var(--muted); text-decoration: none; }
  .logs:hover { color: var(--accent); }
  td a:not(.name):not(.logs) { color: var(--text); text-decoration: none; }
  td a:not(.name):not(.logs):hover { color: var(--accent); }
  .none { color: var(--muted); padding: var(--space-3) 12px; margin: 0; }

  @media (max-width: 720px) {
    table, tbody, tr, td { display: block; }
    thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
    tr { border-bottom: 1px solid var(--border); padding: var(--space-2); }
    td { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); height: auto; border-bottom: 0; padding: var(--space-1) var(--space-2); }
    td::before { content: attr(data-label); color: var(--muted); }
    td[data-label='Container']::before, .row-actions::before { content: none; }
    .row-actions { width: auto; }
    .image { max-width: 55vw; }
    .search, .search input { width: 100%; }
    .search { flex: 1 1 100%; }
  }
</style>
