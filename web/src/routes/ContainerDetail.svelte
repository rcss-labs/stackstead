<script lang="ts">
  import { onDestroy, onMount, untrack } from 'svelte';
  import { api } from '$lib/api/client';
  import type { ContainerDetail } from '$lib/api/types';
  import Banner from '$lib/components/Banner.svelte';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import Tabs from '$lib/components/Tabs.svelte';
  import { createContainerList } from '$lib/containers/list';
  import ContainerActions from '$lib/containers/ContainerActions.svelte';
  import { formatBytes, formatPercent } from '$lib/format';
  import { link, navigate, route } from '$lib/router';
  import LogsViewer from '$lib/stacks/LogsViewer.svelte';
  import { debounce } from '$lib/stores/debounce';
  import { changes } from '$lib/stores/live';

  let { name }: { name: string } = $props();

  const tabs = [
    { id: 'details', label: 'Details' },
    { id: 'logs', label: 'Logs' },
  ];
  const requested = $derived($route.query.get('tab'));
  const activeTab = $derived(tabs.find((t) => t.id === requested)?.id ?? 'details');

  // The page owns its list instance: it resolves the name and supplies the live CPU/memory values.
  const list = createContainerList({ list: () => api.containers(), changes });
  const rows = list.rows;
  const listError = list.error;
  onMount(() => list.start());
  onDestroy(() => list.stop());

  const row = $derived($rows?.find((c) => c.name === name));
  // Distinguishes "never existed" from "was here and is now gone".
  let seen = $state(false);
  $effect(() => {
    if (row) seen = true;
  });

  $effect(() => {
    document.title = `${name} · Stackstead`;
  });

  let detail = $state.raw<ContainerDetail | undefined>();
  let detailError = $state('');
  let seq = 0;
  async function loadDetail(id: string) {
    const mine = ++seq;
    try {
      const d = await api.container(id);
      if (mine !== seq) return;
      detail = d;
      detailError = '';
    } catch (e) {
      if (mine !== seq) return;
      detailError = e instanceof Error ? e.message : String(e);
    }
  }

  // A recreated container keeps its name but not its id: drop the old details and load the new ones.
  // Derived from a primitive so a list refresh that changes nothing does not refetch the details.
  const rowId = $derived(row?.id);
  let loadedId: string | undefined;
  $effect(() => {
    const id = rowId;
    const onDetails = activeTab === 'details';
    untrack(() => {
      if (id !== loadedId) {
        loadedId = id;
        detail = undefined;
        detailError = '';
        seq++;
      }
      if (id && onDetails) void loadDetail(id);
    });
  });

  const refetch = debounce(() => {
    if (row && activeTab === 'details') void loadDetail(row.id);
  });
  onDestroy(() => refetch.cancel());
  let firstChange = true;
  $effect(() => {
    const c = $changes;
    untrack(() => {
      if (firstChange) {
        firstChange = false;
        return;
      }
      if (!c) return;
      if (c.resync) refetch.flush();
      else if (!row?.project || c.project === row.project) refetch.trigger();
    });
  });

  const composeFirst = (labels: Record<string, string>) =>
    Object.entries(labels).sort(([a], [b]) => {
      const ca = a.startsWith('com.docker.compose.') ? 0 : 1;
      const cb = b.startsWith('com.docker.compose.') ? 0 : 1;
      return ca - cb || a.localeCompare(b);
    });

  const containerUrl = (tab: string) => `/containers/${encodeURIComponent(name)}?tab=${tab}`;
  function selectTab(id: string) {
    if (id !== activeTab) navigate(containerUrl(id));
  }
  // The limit comes from the container's own config; the stats limit is host RAM for unlimited containers.
  function memoryText(used: number | null, limit: number | null): string {
    if (used === null) return '—';
    return limit ? `${formatBytes(used)} / ${formatBytes(limit)}` : formatBytes(used);
  }
  const unknownTime = (iso: string | null) => !iso || iso.startsWith('0001-');
</script>

{#if $rows === undefined}
  {#if $listError}
    <Banner kind="danger" title="Could not load the container">{#snippet children()}{$listError}{/snippet}</Banner>
    <p><Button variant="secondary" onclick={() => list.refresh()}>Retry</Button></p>
  {:else}
    <Spinner />
  {/if}
{:else if !row}
  {#if seen}
    <EmptyState title="This container no longer exists" description={`"${name}" was removed.`}>
      <a href="/containers" use:link>Back to containers</a>
    </EmptyState>
  {:else}
    <EmptyState title="Container not found" description={`There is no container named "${name}".`}>
      <a href="/containers" use:link>Back to containers</a>
    </EmptyState>
  {/if}
{:else}
  {@const c = row}
  {@const fullId = detail?.id ?? c.id}
  <section class="page">
    {#if $listError}
      <Banner kind="warning" title="Container list out of date">{#snippet children()}Showing the last known state; {$listError}{/snippet}</Banner>
    {/if}
    <header class="head">
      <div class="row1">
        <h1>{c.name}</h1>
        <StatusBadge status={c.state} />
        {#if c.health !== 'none'}<span class="health {c.health}"><i class="dot"></i>{c.health}</span>{/if}
        <p class="meta">
          <span class="mono">{c.image}</span> ·
          {#if c.managed === 'stack'}
            stack <a href={`/stacks/${encodeURIComponent(c.stack)}`} use:link>{c.stack}</a>
          {:else if c.managed === 'compose'}
            project <span class="mono">{c.project}</span> <span class="tag">unregistered</span>
          {:else}
            not in a stack
          {/if}
        </p>
        <div class="actions" role="group" aria-label="Container actions">
          {#if c.managed === 'stack'}
            <a class="open-stack" href={`/stacks/${encodeURIComponent(c.stack)}`} use:link>Open stack</a>
            <span class="note">Managed by stack {c.stack}; use the stack's actions.</span>
          {:else}
            <ContainerActions row={c} size="md" onchanged={() => list.refresh()} />
          {/if}
        </div>
      </div>
      <div class="row2">
        <span class="mono id" title={fullId}>{fullId.slice(0, 12)}</span>
        <CopyButton text={fullId} label="Copy id" />
      </div>
    </header>

    <Tabs {tabs} selected={activeTab} onselect={selectTab} ariaLabel="Container sections">
      {#snippet children(tab)}
        {#if tab === 'details'}
          <div class="cols">
            <div class="card">
              {#if detailError}
                <Banner kind={detail ? 'warning' : 'danger'} title={detail ? 'Showing the last loaded details' : 'Could not load the details'}>
                  {#snippet children()}{detailError}{/snippet}
                </Banner>
              {/if}
              {#if detail}
                {@const d = detail}
                <dl>
                  <dt>State</dt>
                  <dd>{d.state} · {d.status}</dd>
                  <dt>Started</dt>
                  <dd>{#if unknownTime(d.startedAt)}—{:else}<RelativeTime iso={d.startedAt!} />{/if}</dd>
                  <dt>Finished</dt>
                  <dd>
                    {#if unknownTime(d.finishedAt)}—{:else}<RelativeTime iso={d.finishedAt!} />{/if}
                    {#if d.exitCode !== null}<span class="mono">exit {d.exitCode}</span>{/if}
                  </dd>
                  <dt>Restart policy</dt>
                  <dd>{d.restartPolicy || '—'}</dd>
                  <dt>Restarts 24h</dt>
                  <dd class="mono">{d.restarts24h}</dd>
                  <dt>Ports</dt>
                  <dd>
                    {#each d.ports as p (p)}<span class="chip mono">{p}</span>{:else}—{/each}
                  </dd>
                  <dt>Networks</dt>
                  <dd>
                    {#each Object.entries(d.networks) as [net, ip] (net)}<div class="mono">{net} → {ip || '—'}</div>{:else}—{/each}
                  </dd>
                  <dt>Mounts</dt>
                  <dd>
                    {#each d.mounts as m (m.destination)}<div class="mono">{m.type} {m.source} → {m.destination}{m.readOnly ? ' (ro)' : ''}</div>{:else}—{/each}
                  </dd>
                </dl>
                {#if Object.keys(d.labels).length > 0}
                  <details class="labels">
                    <summary>Labels ({Object.keys(d.labels).length})</summary>
                    <dl>
                      {#each composeFirst(d.labels) as [k, v] (k)}
                        <dt class="mono">{k}</dt>
                        <dd class="mono">{v}</dd>
                      {/each}
                    </dl>
                  </details>
                {/if}
              {:else if !detailError}
                <Spinner />
              {/if}
            </div>
            <section class="card resources" aria-label="Resources · now">
              <h2>Resources · now</h2>
              <dl>
                <dt>CPU</dt>
                <dd class="mono">{c.cpu !== null ? formatPercent(c.cpu) : '—'}</dd>
                <dt>Memory</dt>
                <dd class="mono">
                  {memoryText(c.mem, detail?.memoryLimit ?? null)}
                </dd>
              </dl>
            </section>
          </div>
        {:else if tab === 'logs'}
          {#key c.id}
            <LogsViewer source={{ kind: 'container', id: c.id }} services={[]} />
          {/key}
        {/if}
      {/snippet}
    </Tabs>
  </section>
{/if}

<style>
  .page { display: flex; flex-direction: column; gap: var(--space-4); }
  .head { display: flex; flex-direction: column; gap: 10px; }
  .row1 { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
  h1 { margin: 0; font-family: var(--mono); font-size: 20px; font-weight: 600; overflow-wrap: anywhere; }
  .meta { margin: 0; color: var(--muted); font-size: 12.5px; overflow-wrap: anywhere; }
  .actions { margin-inline-start: auto; display: flex; align-items: center; gap: var(--space-3); flex-wrap: wrap; }
  .open-stack {
    display: inline-flex; align-items: center; height: 32px; padding: 0 12px; border: 1px solid var(--border-strong);
    border-radius: var(--radius); color: var(--text); text-decoration: none; font-size: 13px; font-weight: 500;
  }
  .open-stack:hover { background: var(--hover); }
  .note { color: var(--muted); font-size: 12.5px; }
  .row2 { display: flex; align-items: center; flex-wrap: nowrap; gap: 8px; color: var(--muted); font-size: 12.5px; min-width: 0; }
  .id { white-space: nowrap; }
  .row2 :global(button) { white-space: nowrap; flex: none; }
  .mono { font-family: var(--mono); }
  .tag { font-size: 11px; color: var(--muted); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 0 5px; }
  .health { display: inline-flex; align-items: center; font-size: 11px; color: var(--muted); }
  .health.healthy { color: var(--success); }
  .health.unhealthy { color: var(--danger); }
  .health.starting { color: var(--warning); }
  .dot { display: inline-block; width: 6px; height: 6px; border-radius: 50%; background: currentColor; margin-right: 4px; }
  .cols { display: grid; grid-template-columns: minmax(0, 1fr); gap: var(--space-4); align-items: start; }
  @media (min-width: 900px) { .cols { grid-template-columns: minmax(0, 1fr) 260px; } }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); padding: var(--space-4); min-width: 0; }
  .card h2 { margin: 0 0 var(--space-3); font-size: 13px; font-weight: 600; }
  dl { display: grid; grid-template-columns: max-content minmax(0, 1fr); gap: 8px 16px; margin: 0; font-size: 13px; }
  dt { color: var(--muted); }
  dd { margin: 0; overflow-wrap: anywhere; min-width: 0; }
  .chip { font-size: 11px; background: var(--raised); border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 0 5px; margin-right: 4px; }
  .labels { margin-top: var(--space-3); }
  .labels summary { cursor: pointer; color: var(--muted); font-size: 12.5px; margin-bottom: var(--space-2); }
  @media (max-width: 480px) { dl { grid-template-columns: minmax(0, 1fr); gap: 2px; } dd { margin-bottom: 8px; } }
</style>
