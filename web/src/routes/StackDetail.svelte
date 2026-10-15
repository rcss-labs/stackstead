<script lang="ts">
  import { onDestroy, untrack } from 'svelte';
  import { api, ApiError } from '$lib/api/client';
  import type { Action, Operation, ServiceDetail, StackDetail } from '$lib/api/types';
  import Banner from '$lib/components/Banner.svelte';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import StatusBadge from '$lib/components/StatusBadge.svelte';
  import Tabs from '$lib/components/Tabs.svelte';
  import { link, navigate, route } from '$lib/router';
  import { ICONS } from '$lib/shell/icons';
  import ActionsBar from '$lib/stacks/ActionsBar.svelte';
  import { lifecycleDisabledReason } from '$lib/stacks/lifecycle';
  import { createStackLiveMetrics } from '$lib/stacks/liveMetrics';
  import LogsViewer from '$lib/stacks/LogsViewer.svelte';
  import OperationPanel from '$lib/stacks/OperationPanel.svelte';
  import OperationsList from '$lib/stacks/OperationsList.svelte';
  import ServiceCard from '$lib/stacks/ServiceCard.svelte';
  import StackAside from '$lib/stacks/StackAside.svelte';
  import { debounce } from '$lib/stores/debounce';
  import { changes, live, refreshStatus } from '$lib/stores/live';
  import { setUnsavedChanges } from '$lib/stores/guard';
  import { mediaQuery } from '$lib/stores/media';
  import { operationsRefresh, refreshRunningOps, refreshStacks } from '$lib/stores/stacks';
  import { toasts } from '$lib/stores/toasts';

  let { name }: { name: string } = $props();

  // Heavy editors and charts load on demand so the entry chunk stays small.
  const loadComposeEditor = () => import('$lib/stacks/ComposeEditor.svelte');
  const loadServiceEditor = () => import('$lib/stacks/ServiceEditor.svelte');
  const loadMetricsPanel = () => import('$lib/stacks/MetricsPanel.svelte');

  type TabId = 'services' | 'compose' | 'logs' | 'metrics' | 'operations';
  const ALL_TABS: { id: TabId; label: string }[] = [
    { id: 'services', label: 'Services' },
    { id: 'compose', label: 'Compose' },
    { id: 'logs', label: 'Logs' },
    { id: 'metrics', label: 'Metrics' },
    { id: 'operations', label: 'Operations' },
  ];

  let detail = $state.raw<StackDetail | undefined>();
  let notFound = $state(false);
  let loadError = $state('');
  let operations = $state.raw<Operation[]>([]);
  let acting = $state(false);
  let removeOpId = $state<string | undefined>();

  let editingService = $state<string | undefined>();
  let serviceDirty = $state(false);
  let composeDirty = $state(false);
  onDestroy(() => setUnsavedChanges(false));

  const parseError = $derived(detail?.parseError);
  // A parse error means the structured views are meaningless: raw editor only.
  const missingFile = $derived(!!parseError && /no such file|not exist|not found|cannot find/i.test(parseError));
  const tabs = $derived.by(() => {
    const visible = parseError
      ? ALL_TABS.filter((t) => (t.id === 'compose' && !missingFile) || t.id === 'operations')
      : ALL_TABS;
    return visible.map((t) => ({
      ...t,
      badge: t.id === 'services' ? detail?.serviceCount : t.id === 'operations' ? operations.length : undefined,
    }));
  });
  const requested = $derived($route.query.get('tab'));
  const activeTab = $derived<TabId>(
    (tabs.find((t) => t.id === requested)?.id ?? tabs[0]?.id ?? 'operations') as TabId,
  );
  const selectedOp = $derived($route.query.get('op') ?? undefined);

  // An editor's flag only counts while that editor is mounted; unmounting without the confirm path
  // (browser Back, a parse error appearing) must not leave a stale "unsaved" state behind.
  const dirty = $derived(
    (activeTab === 'compose' && composeDirty) || (activeTab === 'services' && !!editingService && serviceDirty),
  );
  let pendingLeave = $state<(() => void) | undefined>();

  $effect(() => {
    setUnsavedChanges(dirty);
  });
  $effect(() => {
    if (activeTab !== 'compose') composeDirty = false;
    if (activeTab !== 'services' || !editingService) serviceDirty = false;
  });

  const hasRunning = $derived(operations.some((o) => o.state === 'running'));
  const lifecycleReason = $derived(lifecycleDisabledReason({ live: $live, parseError }));

  // One live-metrics stream for the whole page, shared by the aside and the Metrics tab; it is open only
  // while one of them is showing. The page is keyed by stack name, so the name is read once on purpose.
  /* svelte-ignore state_referenced_locally */
  const liveMetrics = createStackLiveMetrics(name);
  onDestroy(() => liveMetrics.close());
  const wide = mediaQuery('(min-width: 1100px)');
  const asideShown = $derived($wide && !!detail && !parseError);
  const needsLive = $derived(asideShown || activeTab === 'metrics');
  $effect(() => {
    if (!needsLive) return;
    return liveMetrics.acquire();
  });

  $effect(() => {
    document.title = `${name} · Stackstead`;
  });

  let seq = 0;
  async function load(): Promise<StackDetail | undefined> {
    const mine = ++seq;
    try {
      const d = await api.stack(name);
      if (mine !== seq) return undefined;
      detail = d;
      notFound = false;
      loadError = '';
      return d;
    } catch (e) {
      if (mine !== seq) return undefined;
      if (e instanceof ApiError && e.status === 404) {
        // The stack vanished right after we asked for its removal: that is the removal succeeding.
        if (removeOpId) finishRemoval();
        else notFound = true;
      }
      else loadError = e instanceof Error ? e.message : String(e);
      return undefined;
    }
  }
  let opsSeq = 0;
  async function loadOperations() {
    const mine = ++opsSeq;
    try {
      const list = await api.stackOperations(name);
      if (mine === opsSeq) operations = list;
    } catch {
      // The list is secondary; the next refresh tries again.
    }
  }

  // Initial load, then refetch on this project's changes (debounced: a burst of container events is one
  // refetch) and immediately on every resync.
  const refetch = debounce(() => {
    void load();
    void loadOperations();
  });
  onDestroy(() => refetch.cancel());
  let firstChange = true;
  $effect(() => {
    const c = $changes;
    untrack(() => {
      if (firstChange) {
        firstChange = false;
        void load();
        void loadOperations();
        return;
      }
      if (!c) return;
      if (c.resync) refetch.flush();
      else if (c.project === detail?.project) refetch.trigger();
    });
  });
  let firstTick = true;
  $effect(() => {
    void $operationsRefresh;
    if (firstTick) {
      firstTick = false;
      return;
    }
    untrack(() => void loadOperations());
  });

  function guarded(action: () => void) {
    if (dirty) pendingLeave = action;
    else action();
  }
  function discardAndLeave() {
    const action = pendingLeave;
    pendingLeave = undefined;
    serviceDirty = false;
    composeDirty = false;
    editingService = undefined;
    action?.();
  }

  function stackUrl(params: Record<string, string | undefined>): string {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
    return `/stacks/${encodeURIComponent(name)}?${q.toString()}`;
  }
  function selectTab(id: string) {
    if (id === activeTab) return;
    guarded(() => {
      editingService = undefined;
      navigate(stackUrl({ tab: id }));
    });
  }
  function openOperation(id: string, replace = false) {
    navigate(stackUrl({ tab: 'operations', op: id }), { replace });
    void loadOperations();
  }

  async function startAction(action: Action) {
    if (acting) return;
    acting = true;
    try {
      const o = await api.act(name, action);
      editingService = undefined;
      serviceDirty = false;
      composeDirty = false;
      openOperation(o.id);
      refreshRunningOps();
    } catch (e) {
      reportActionError(e, `Could not run ${action}`);
    } finally {
      acting = false;
    }
  }
  async function removeStack(withVolumes: boolean) {
    if (acting) return;
    acting = true;
    try {
      const o = await api.remove(name, withVolumes);
      removeOpId = o.id;
      editingService = undefined;
      serviceDirty = false;
      composeDirty = false;
      openOperation(o.id);
      refreshRunningOps();
    } catch (e) {
      reportActionError(e, 'Could not remove the stack');
    } finally {
      acting = false;
    }
  }
  function reportActionError(e: unknown, prefix: string) {
    const message = e instanceof Error ? e.message : String(e);
    if (e instanceof ApiError && e.status === 503) void refreshStatus();
    toasts.push({ kind: e instanceof ApiError && e.status === 409 ? 'warning' : 'danger', message: `${prefix}: ${message}` });
  }

  async function cancelOperation(id: string) {
    try {
      await api.cancel(id);
    } catch (e) {
      toasts.push({ kind: 'danger', message: `Could not cancel: ${e instanceof Error ? e.message : String(e)}` });
    }
  }
  let removalDone = false;
  function finishRemoval() {
    if (removalDone) return;
    removalDone = true;
    toasts.push({ kind: 'success', message: `Removed ${name}` });
    refreshRunningOps();
    void refreshStacks();
    navigate('/');
  }
  function operationFinished(o: Operation) {
    refreshRunningOps();
    void refreshStacks();
    if (o.id === removeOpId) {
      if (o.state === 'succeeded') {
        finishRemoval();
      } else {
        removeOpId = undefined;
        toasts.push({ kind: 'danger', message: `Removing ${name} ${o.state.replace('_', ' ')}` });
      }
      return;
    }
    void load();
    void loadOperations();
  }

  async function reloadDetail(): Promise<StackDetail> {
    const mine = ++seq;
    const d = await api.stack(name);
    if (mine === seq) detail = d;
    return d;
  }
  function afterSave() {
    void load();
  }

  function editService(s: ServiceDetail) {
    guarded(() => (editingService = s.name));
  }
  function toRawEditor() {
    guarded(() => {
      editingService = undefined;
      navigate(stackUrl({ tab: 'compose' }));
    });
  }

  const serviceLogsHref = (service: string) => stackUrl({ tab: 'logs', service });
  function openLogs(service: string) {
    guarded(() => {
      editingService = undefined;
      navigate(serviceLogsHref(service));
    });
  }
</script>

{#snippet notFoundView()}
  <EmptyState title="Stack not found" description={`There is no stack named "${name}".`}>
    <a href="/" use:link>Back to stacks</a>
  </EmptyState>
{/snippet}

{#if notFound}
  {@render notFoundView()}
{:else if !detail}
  {#if loadError}
    <Banner kind="danger" title="Could not load the stack">{#snippet children()}{loadError}{/snippet}</Banner>
    <p><Button variant="secondary" onclick={() => void load()}>Retry</Button></p>
  {:else}
    <Spinner />
  {/if}
{:else}
  {@const d = detail}
  <section class="detail">
    <header class="head">
      <div class="row1">
        <h1>{d.name}</h1>
        <StatusBadge status={d.status} />
        <p class="meta">
          {d.running}/{d.serviceCount} services up · <span>{d.origin}</span> · project <span class="mono">{d.project}</span>
        </p>
        <div class="actions">
          <ActionsBar
            stack={d}
            lifecycleDisabledReason={lifecycleReason}
            busy={hasRunning || acting}
            onact={(a) => guarded(() => void startAction(a))}
            onremove={(v) => guarded(() => void removeStack(v))}
          />
        </div>
      </div>
      <div class="row2">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={ICONS.file}></path></svg>
        <span class="mono path">{d.path}</span>
        <CopyButton text={d.path} variant="icon" label="Copy path" />
      </div>
    </header>

    {#if loadError}
      <Banner kind="warning" title="Showing the last loaded state">{#snippet children()}{loadError}{/snippet}</Banner>
    {/if}
    {#if parseError}
      <Banner kind="danger" title={missingFile ? 'Compose file is missing' : 'Compose file cannot be parsed'}>
        {#snippet children()}
          <pre class="perr">{parseError}</pre>
          {missingFile ? 'Restore the file on disk to edit this stack.' : 'Fix it in the Compose tab; the structured editor is unavailable until it parses.'}
        {/snippet}
      </Banner>
    {/if}

    <div class="cols" class:with-aside={asideShown}>
    <div class="main">
    <Tabs {tabs} selected={activeTab} onselect={selectTab} ariaLabel="Stack sections">
      {#snippet children(tab)}
        {#if tab === 'services'}
          <div class="services">
            {#each d.services as svc (svc.name)}
              {#if editingService === svc.name}
                {#key `${d.name}/${svc.name}`}
                  {#await loadServiceEditor()}
                    <Spinner />
                  {:then m}
                    <m.default
                      stack={d}
                      service={svc}
                      bind:dirty={serviceDirty}
                      onsaved={afterSave}
                      onapplied={() => {
                        void loadOperations();
                      }}
                      onRawEditor={toRawEditor}
                      onreload={reloadDetail}
                      lifecycleDisabledReason={lifecycleReason}
                    />
                  {/await}
                {/key}
                <p>
                  <Button variant="ghost" onclick={() => guarded(() => (editingService = undefined))}>Close editor</Button>
                </p>
              {:else}
                <ServiceCard
                  stack={d.name}
                  service={svc}
                  onedit={() => editService(svc)}
                  logsHref={serviceLogsHref(svc.name)}
                  onlogs={() => openLogs(svc.name)}
                />
              {/if}
            {:else}
              <EmptyState title="No services" description="This compose file defines no services." />
            {/each}
          </div>
        {:else if tab === 'compose'}
          {#key d.name}
            {#await loadComposeEditor()}
              <Spinner />
            {:then m}
              <m.default
                stackName={d.name}
                {parseError}
                composePath={d.path}
                bind:dirty={composeDirty}
                onsaved={afterSave}
                onapply={() => void startAction('up')}
              />
            {/await}
          {/key}
        {:else if tab === 'logs'}
          {#key d.name}
            <LogsViewer stackName={d.name} services={d.services.map((s) => s.name)} service={$route.query.get('service') ?? undefined} />
          {/key}
        {:else if tab === 'metrics'}
          {#key d.name}
            {#await loadMetricsPanel()}
              <Spinner />
            {:then m}
              <m.default stackName={d.name} metricsDegraded={$live.metricsDegraded} live={liveMetrics}
                metricsIntervalSeconds={$live.metricsIntervalSeconds}
                metricsRetentionSeconds={$live.metricsRetentionSeconds}
              />
            {/await}
          {/key}
        {:else if tab === 'operations'}
          <div class="ops">
            <OperationsList {operations} selected={selectedOp} href={(id) => stackUrl({ tab: 'operations', op: id })} />
            {#if selectedOp}
              {#key selectedOp}
                <OperationPanel operationId={selectedOp} oncancel={cancelOperation} onfinished={operationFinished} />
              {/key}
            {/if}
          </div>
        {/if}
      {/snippet}
    </Tabs>
    </div>
    {#if asideShown}
      <StackAside
        detail={d}
        {operations}
        live={liveMetrics}
        opsHref={stackUrl({ tab: 'operations' })}
        opHref={(id) => stackUrl({ tab: 'operations', op: id })}
      />
    {/if}
    </div>
  </section>
{/if}

<ConfirmDialog
  open={pendingLeave !== undefined}
  title="Discard unsaved changes?"
  message="You have unsaved changes in the editor. Leaving now discards them."
  confirmLabel="Discard changes"
  danger
  onconfirm={discardAndLeave}
  oncancel={() => (pendingLeave = undefined)}
/>

<style>
  .detail { display: flex; flex-direction: column; gap: var(--space-4); }
  .head { display: flex; flex-direction: column; gap: 10px; }
  .row1 { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
  h1 { margin: 0; font-family: var(--mono); font-size: 20px; font-weight: 600; overflow-wrap: anywhere; }
  .meta { margin: 0; color: var(--muted); font-size: 12.5px; }
  .actions { margin-inline-start: auto; }
  .row2 { display: flex; align-items: flex-start; gap: 8px; color: var(--muted); font-size: 12.5px; min-width: 0; }
  .row2 svg { flex: none; margin-top: 2px; }
  .mono { font-family: var(--mono); }
  .path { overflow-wrap: anywhere; min-width: 0; }
  .cols { display: grid; grid-template-columns: minmax(0, 1fr); gap: 16px; align-items: start; }
  .cols.with-aside { grid-template-columns: minmax(0, 1fr) 300px; }
  .main { min-width: 0; }
  .services { display: flex; flex-direction: column; gap: var(--space-3); padding-top: var(--space-3); }
  .ops { display: flex; flex-direction: column; gap: var(--space-4); padding-top: var(--space-3); }
  .perr { white-space: pre-wrap; overflow-wrap: anywhere; margin: var(--space-2) 0; }
</style>
