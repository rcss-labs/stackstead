<script lang="ts">
  import { api } from '$lib/api/client';
  import type { Network } from '$lib/api/types';
  import Banner from '$lib/components/Banner.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import CreateNetworkDialog from '$lib/networks/CreateNetworkDialog.svelte';
  import { createNetworkList, networkOwner, removeDisabledReason, sortNetworks } from '$lib/networks/networks';
  import { link } from '$lib/router';
  import { changes } from '$lib/stores/live';
  import { toasts } from '$lib/stores/toasts';

  const list = createNetworkList({ list: api.networks, changes });
  const { rows, error } = list;
  $effect(() => { list.start(); return () => list.stop(); });
  const networks = $derived(sortNetworks($rows ?? []));
  const uid = $props.id();
  let expanded = $state<Record<string, boolean>>({});
  let creating = $state(false);
  let pending = $state<Network>();
  let removing = $state<string>();

  async function remove() {
    if (!pending || removing) return;
    const network = pending;
    pending = undefined;
    removing = network.id;
    try {
      await api.deleteNetwork(network.id);
      toasts.push({ kind: 'success', message: `Network \`${network.name}\` removed` });
    } catch (error) {
      toasts.push({ kind: 'danger', message: error instanceof Error ? error.message : String(error) });
    } finally {
      removing = undefined;
      list.refresh();
    }
  }
</script>

<section>
  <header class="head">
    <h1 id="networks-heading">Networks</h1>
    {#if $rows}<span class="counts">{networks.length} networks</span>{/if}
    <div class="create"><Button variant="primary" onclick={() => creating = true}>Create network</Button></div>
  </header>
  {#if $error && !$rows}
    <Banner kind="danger" title="Could not load networks">{$error}</Banner>
    <p><Button variant="secondary" onclick={() => list.refresh()}>Retry</Button></p>
  {:else if !$rows}
    <div class="card pad"><Spinner label="Loading networks" /></div>
  {:else if networks.length === 0}
    <EmptyState title="No networks" description="Networks appear here as soon as Docker creates them." />
  {:else}
    <div class="card">
      <!-- Explicit roles preserve table semantics in the mobile card layout. -->
      <!-- svelte-ignore a11y_no_redundant_roles -->
      <table role="table" aria-labelledby="networks-heading">
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <thead role="rowgroup">
          <!-- svelte-ignore a11y_no_redundant_roles -->
          <tr role="row">
            {#each ['Name', 'Driver', 'Subnet', 'Gateway', 'Parent', 'Containers', 'Owner', 'Actions'] as label}
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <th role="columnheader" scope="col">{label}</th>
            {/each}
          </tr>
        </thead>
        <!-- svelte-ignore a11y_no_redundant_roles -->
        <tbody role="rowgroup">
          {#each networks as n (n.id)}
            {@const owner = networkOwner(n)}
            {@const detailID = `${uid}-containers-${encodeURIComponent(n.id)}`}
            <!-- svelte-ignore a11y_no_redundant_roles -->
            <tr role="row">
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Name"><span class="name mono">{n.name}</span>
                {#if n.containers.length > 0}
                  <button class="expand" type="button" aria-label={`Show containers on ${n.name}`} title={`Show containers on ${n.name}`} aria-expanded={Boolean(expanded[n.id])} aria-controls={detailID} onclick={() => expanded[n.id] = !expanded[n.id]}>
                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d={expanded[n.id] ? 'M6 15l6-6 6 6' : 'M6 9l6 6 6-6'}></path></svg>
                  </button>
                {/if}
              </td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Driver">{n.driver}</td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Subnet"><span class="mono">{n.subnets[0] || '—'}</span>{#if n.subnets.length > 1}<span class="more" title={n.subnets.join(', ')}>+{n.subnets.length - 1}</span>{/if}</td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Gateway"><span class="mono">{n.gateways[0] || '—'}</span></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Parent"><span class="mono">{n.parent || '—'}</span></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Containers"><span class="mono">{n.containers.length}</span></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Owner">
                {#if owner.kind === 'stack'}<a class="tag" href={`/stacks/${encodeURIComponent(owner.stack)}`} use:link>stack {owner.stack}</a>
                {:else if owner.kind === 'compose'}<span class="tag">compose {owner.project}</span>
                {:else}<span class="tag">{owner.kind === 'builtin' ? 'built-in' : 'unmanaged'}</span>{/if}
              </td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <td role="cell" data-label="Actions" class="row-actions"><Button variant="danger" size="sm" disabledReason={removeDisabledReason(n) ?? (removing ? 'Removing network' : undefined)} loading={removing === n.id} onclick={() => pending = n}>Remove</Button></td>
            </tr>
            {#if expanded[n.id] && n.containers.length > 0}
              <!-- svelte-ignore a11y_no_redundant_roles -->
              <tr role="row" id={detailID} class="detail">
                <!-- svelte-ignore a11y_no_redundant_roles -->
                <td role="cell" colspan="8" data-label="Attached containers">
                  {#each n.containers as container}
                    <div class="endpoint"><a class="mono" href={`/containers/${encodeURIComponent(container.name)}`} use:link>{container.name}</a> → <span class="mono">{container.ipv4 || '—'}</span></div>
                  {/each}
                </td>
              </tr>
            {/if}
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</section>
<CreateNetworkDialog open={creating} onclose={() => creating = false} oncreated={(network) => { toasts.push({ kind: 'success', message: `Network \`${network.name}\` created` }); list.refresh(); }} />
<ConfirmDialog open={pending !== undefined} title={`Remove network \`${pending?.name ?? ''}\`?`} message="Docker deletes the network. Containers can only attach again once it is recreated." confirmLabel="Remove" danger onconfirm={() => void remove()} oncancel={() => pending = undefined} />

<style>
  .head { display: flex; flex-wrap: wrap; align-items: baseline; gap: var(--space-3); margin-bottom: var(--space-4); }
  .counts { color: var(--muted); font-size: 14px; font-family: var(--mono); }
  .create { margin-left: auto; }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); overflow-x: auto; }
  .card.pad { padding: var(--space-4); }
  table { width: 100%; border-collapse: collapse; }
  th { text-align: left; font-size: 12px; font-weight: 500; color: var(--muted); padding: var(--space-2) 12px; border-bottom: 1px solid var(--border); white-space: nowrap; }
  td { text-align: left; height: var(--row); padding: var(--pad) 12px; border-bottom: 1px solid var(--border); vertical-align: middle; overflow-wrap: anywhere; }
  tbody tr:hover { background: var(--hover); }
  tbody tr:last-child td { border-bottom: 0; }
  .mono { font-family: var(--mono); font-size: 12px; }
  .name { font-size: 13px; font-weight: 500; }
  .more { margin-left: var(--space-2); color: var(--muted); font-size: 12px; }
  .tag { font-size: 11px; border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 0 5px; }
  a { color: var(--text); text-decoration: none; }
  a:hover { color: var(--accent); }
  .row-actions { width: 1%; }
  .expand { display: inline-grid; place-items: center; vertical-align: middle; width: 30px; height: 30px; margin-left: var(--space-1); border: 1px solid var(--border); border-radius: var(--radius); background: transparent; color: var(--muted); cursor: pointer; }
  .expand:hover { background: var(--hover); color: var(--text); }
  .endpoint { padding: var(--space-1) 0; }
  @media (max-width: 720px) {
    .card { overflow-x: hidden; }
    table, tbody, tr, td { display: block; }
    thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
    tr { border-bottom: 1px solid var(--border); padding: var(--space-2); }
    td { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); height: auto; border-bottom: 0; padding: var(--space-1) var(--space-2); }
    td::before { content: attr(data-label); color: var(--muted); flex-shrink: 0; }
    td[data-label='Name'] { justify-content: flex-start; }
    td[data-label='Name']::before, .row-actions::before { content: none; }
    td > span, td > a { min-width: 0; }
    .row-actions { width: auto; }
    .detail td { display: block; }
    .detail td::before { display: block; }
  }
</style>
