<script lang="ts">
  import { onDestroy, onMount, tick, untrack } from 'svelte';
  import { api, onUnauthorized } from '$lib/api/client';
  import { bootstrapSession, expireSession, logout, session } from '$lib/api/session';
  import Banner from '$lib/components/Banner.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import EmptyState from '$lib/components/EmptyState.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Toasts from '$lib/components/Toasts.svelte';
  import { link, matchRoute, navigate, route, startRouter } from '$lib/router';
  import { debounce } from '$lib/stores/debounce';
  import { changes, live, startLive, stopLive } from '$lib/stores/live';
  import { unsavedChanges, setUnsavedChanges } from '$lib/stores/guard';
  import { operationsRefresh, startStacks } from '$lib/stores/stacks';
  import { toasts } from '$lib/stores/toasts';
  import Rail from '$lib/shell/Rail.svelte';
  import TopBar from '$lib/shell/TopBar.svelte';
  import ContainerDetail from './routes/ContainerDetail.svelte';
  import ContainerList from './routes/ContainerList.svelte';
  import Storage from './routes/Storage.svelte';
  import StorageImage from './routes/StorageImage.svelte';
  import StorageVolume from './routes/StorageVolume.svelte';
  import NetworkList from './routes/NetworkList.svelte';
  import Login, { safeNext } from './routes/Login.svelte';
  import Settings from './routes/Settings.svelte';
  import Setup from './routes/Setup.svelte';
  import StackCreate from './routes/StackCreate.svelte';
  import StackDetail from './routes/StackDetail.svelte';
  import StackImport from './routes/StackImport.svelte';
  import StackList from './routes/StackList.svelte';

  let runningOps = $state(0);
  // The session store may hold a previous state; nothing is decided until this page load has asked the server.
  let booted = $state(false);
  let loggingOut = false;
  let pendingHref = $state<string | undefined>();
  let pendingLogout = $state(false);

  const path = $derived($route.path);
  const storageImageParams = $derived(matchRoute('/storage/images/:id', path));
  const storageVolumeParams = $derived(matchRoute('/storage/volumes/:name', path));
  const stackParams = $derived(matchRoute('/stacks/:name', path));
  const containerParams = $derived(matchRoute('/containers/:name', path));
  const view = $derived.by(() => {
    if (path === '/') return 'list' as const;
    if (storageImageParams) return 'storageImage' as const;
    if (storageVolumeParams) return 'storageVolume' as const;
    if (path === '/storage') return 'storage' as const;
    if (path === '/networks') return 'networks' as const;
    if (path === '/containers') return 'containers' as const;
    if (path === '/stacks/new') return 'create' as const;
    if (path === '/stacks/import') return 'import' as const;
    if (stackParams) return 'detail' as const;
    if (containerParams) return 'container' as const;
    if (path === '/settings') return 'settings' as const;
    return 'notfound' as const;
  });

  function sessionLost() {
    // Already handled (e.g. the events stream got there first): a late 401 must not clobber ?next=.
    if ($session.state !== 'authenticated') return;
    stopLive();
    const here = window.location.pathname + window.location.search;
    expireSession();
    navigate(here.startsWith('/login') || here === '/' ? '/login' : `/login?next=${encodeURIComponent(here)}`, {
      replace: true,
    });
  }

  onMount(() => {
    const stopRouter = startRouter();
    onUnauthorized(sessionLost);
    bootstrapSession()
      .catch(() => {
        toasts.push({ kind: 'danger', message: 'Could not reach the Stackstead server' });
      })
      .finally(() => (booted = true));
    return stopRouter;
  });

  // Session guards. Login and Setup redirect themselves after success.
  $effect(() => {
    const s = $session.state;
    const p = $route.path;
    if (!booted) return;
    if (s === 'needs-setup' && p !== '/setup') {
      navigate('/setup', { replace: true });
    } else if (s === 'anonymous' && p !== '/login') {
      if (loggingOut) navigate('/login', { replace: true });
      else navigate(`/login?next=${encodeURIComponent(p + window.location.search)}`, { replace: true });
    }
    if (s === 'authenticated') {
      loggingOut = false;
      if (p === '/login') navigate(safeNext($route.query.get('next')), { replace: true });
      else if (p === '/setup') navigate('/', { replace: true });
    }
  });

  // The events stream answering 401 means the session is gone even if no API call has noticed yet.
  $effect(() => {
    if ($live.connection === 'unauthorized' && $session.state === 'authenticated') sessionLost();
  });

  // Live updates and the stack list live exactly as long as the session.
  $effect(() => {
    if ($session.state !== 'authenticated') return;
    startLive({ fetchStatus: api.status });
    const stopStacks = startStacks({ list: api.stacks, changes });
    return () => {
      stopStacks();
      stopLive();
    };
  });

  let opsSeq = 0;
  function loadRunningOps() {
    const mine = ++opsSeq;
    api
      .runningOperations()
      .then((ops) => {
        if (mine === opsSeq) runningOps = ops.length;
      })
      .catch(() => {});
  }
  // Every container event is a change; the running-operations badge only needs the settled state.
  const refreshOps = debounce(loadRunningOps);
  onDestroy(() => refreshOps.cancel());
  let opsFirst = true;
  $effect(() => {
    const c = $changes;
    void $operationsRefresh;
    if ($session.state !== 'authenticated') {
      refreshOps.cancel();
      opsFirst = true;
      runningOps = 0;
      return;
    }
    untrack(() => {
      if (opsFirst || c?.resync) {
        opsFirst = false;
        refreshOps.flush();
      } else refreshOps.trigger();
    });
  });

  // A client-side navigation does not move focus by itself, so keyboard and screen-reader users would
  // stay on the clicked link. Move it to the new page's heading (or the main region) after the route renders.
  let mainEl: HTMLElement | undefined = $state();
  let lastPath: string | undefined;
  $effect(() => {
    const p = path;
    if (!mainEl) return;
    const previous = lastPath;
    lastPath = p;
    if (previous === undefined || previous === p) return;
    void tick().then(() => {
      if (!mainEl || mainEl.contains(document.activeElement)) return; // the page already placed focus
      const target = mainEl.querySelector<HTMLElement>('h1') ?? mainEl;
      target.setAttribute('tabindex', '-1');
      target.focus({ preventScroll: true });
    });
  });

  const dockerDown = $derived($live.events === 'disconnected' || $live.docker === 'unavailable');

  $effect(() => {
    const titles: Record<string, string> = {
      list: 'Stacks',
      containers: 'Containers',
      networks: 'Networks',
      storage: 'Storage',
      storageImage: 'Storage',
      storageVolume: 'Storage',
      settings: 'Settings',
      notfound: 'Page not found',
    };
    if ($session.state === 'authenticated' && titles[view]) document.title = `${titles[view]} · Stackstead`;
  });

  function requestLogout() {
    if ($unsavedChanges) pendingLogout = true;
    else void handleLogout();
  }
  async function handleLogout() {
    loggingOut = true;
    setUnsavedChanges(false);
    const { ok } = await logout();
    if (!ok) toasts.push({ kind: 'warning', message: 'Logout may not have completed on the server' });
    navigate('/login', { replace: true });
  }

  // A link click that would leave a page with unsaved editor changes asks first.
  function guardLinks(ev: MouseEvent) {
    if (!$unsavedChanges || ev.defaultPrevented || ev.button !== 0) return;
    if (ev.ctrlKey || ev.metaKey || ev.shiftKey || ev.altKey) return;
    const a = (ev.target as Element | null)?.closest?.('a[href]') as HTMLAnchorElement | null;
    if (!a || (a.target && a.target !== '_self') || a.hasAttribute('download')) return;
    const url = new URL(a.href, window.location.href);
    if (url.origin !== window.location.origin) return;
    if (url.pathname === window.location.pathname && url.search === window.location.search) return;
    ev.preventDefault();
    ev.stopPropagation();
    pendingHref = url.pathname + url.search + url.hash;
  }
  function leave() {
    const href = pendingHref;
    const logoutToo = pendingLogout;
    pendingHref = undefined;
    pendingLogout = false;
    setUnsavedChanges(false);
    if (logoutToo) void handleLogout();
    else if (href) navigate(href);
  }
</script>

{#if !booted || $session.state === 'unknown'}
  <main class="container"><Spinner label="Loading Stackstead" /></main>
{:else if $session.state === 'needs-setup'}
  <main class="bare"><Setup /></main>
{:else if $session.state === 'anonymous'}
  <main class="bare"><Login /></main>
{:else}
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <div class="layout" onclickcapture={guardLinks}>
    <Rail username={$session.username} onlogout={requestLogout} current={path} />
    <div class="column">
    <TopBar {path} {runningOps} />
    <div class="banners">
      {#if dockerDown}
        <Banner kind="danger" title="Docker is unreachable">
          {#snippet children()}Status may be stale; lifecycle actions are disabled.{/snippet}
        </Banner>
      {/if}
      {#if $live.compose === 'unavailable'}
        <Banner kind="warning" title="docker compose plugin not found">
          {#snippet children()}Read-only mode: stacks can be viewed but not started or stopped.{/snippet}
        </Banner>
      {/if}
      {#if $live.metricsDegraded}
        <Banner kind="warning" title="Metrics are degraded">
          {#snippet children()}Charts may have gaps until collection recovers.{/snippet}
        </Banner>
      {/if}
      {#if $live.connection === 'reconnecting'}
        <Banner kind="info" title="Live updates reconnecting">
          {#snippet children()}Showing the last known state.{/snippet}
        </Banner>
      {/if}
    </div>
    <main bind:this={mainEl}>
      {#if view === 'list'}
        <StackList />
      {:else if view === 'storageImage' && storageImageParams}
        {#key storageImageParams.id}<StorageImage id={storageImageParams.id} />{/key}
      {:else if view === 'storageVolume' && storageVolumeParams}
        {#key storageVolumeParams.name}<StorageVolume name={storageVolumeParams.name} />{/key}
      {:else if view === 'storage'}
        <Storage />
      {:else if view === 'networks'}
        <NetworkList />
      {:else if view === 'containers'}
        <ContainerList />
      {:else if view === 'container' && containerParams}
        {#key containerParams.name}
          <ContainerDetail name={containerParams.name} />
        {/key}
      {:else if view === 'create'}
        <StackCreate />
      {:else if view === 'import'}
        <StackImport />
      {:else if view === 'detail' && stackParams}
        {#key stackParams.name}
          <StackDetail name={stackParams.name} />
        {/key}
      {:else if view === 'settings'}
        <Settings />
      {:else}
        <EmptyState title="Page not found" description="That page does not exist.">
          <a href="/" use:link>Back to stacks</a>
        </EmptyState>
      {/if}
    </main>
    </div>
  </div>
  <ConfirmDialog
    open={pendingHref !== undefined || pendingLogout}
    title="Discard unsaved changes?"
    message="You have unsaved changes in the editor. Leaving now discards them."
    confirmLabel="Discard changes"
    danger
    onconfirm={leave}
    oncancel={() => {
      pendingHref = undefined;
      pendingLogout = false;
    }}
  />
{/if}
<Toasts />

<style>
  .layout { display: flex; min-height: 100vh; }
  .column { flex: 1; min-width: 0; display: flex; flex-direction: column; }
  .banners { display: flex; flex-direction: column; gap: var(--space-2); padding: 16px 20px 0; }
  .banners:not(:has(*)) { display: none; }
  main { flex: 1; min-width: 0; padding: 20px 20px 40px; }
  main.bare { padding: 0; }
  @media (max-width: 720px) {
    .banners { padding: 12px 16px 0; }
    main { padding: 20px 16px 40px; }
    main.bare { padding: 0; }
  }
</style>
