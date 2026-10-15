<script lang="ts">
  import { link } from '../router';
  import { live } from '../stores/live';
  import { breadcrumb } from './breadcrumb';

  let { path, runningOps }: { path: string; runningOps: number } = $props();

  const crumbs = $derived(breadcrumb(path));
  const dockerOk = $derived($live.docker === 'ok');
  const dockerText = $derived(
    dockerOk ? ($live.dockerVersion ? `Docker ${$live.dockerVersion}` : 'Docker') : 'Docker unavailable',
  );
  const composeText = $derived(
    $live.compose === 'unavailable' ? 'compose unavailable' : `compose ${$live.compose}`,
  );
</script>

<header class="topbar">
  <nav aria-label="Breadcrumb" class="crumbs">
    {#each crumbs as crumb, i (i)}
      {@const last = i === crumbs.length - 1}
      {#if i > 0}<span class="sep" aria-hidden="true">/</span>{/if}
      {#if crumb.href && !last}
        <a class="crumb" href={crumb.href} use:link>{crumb.label}</a>
      {:else}
        <span class="crumb current" class:mono={crumb.mono} aria-current={last ? 'page' : undefined}>{crumb.label}</span>
      {/if}
    {/each}
  </nav>
  <span class="spacer"></span>
  {#if runningOps > 0}
    <span class="pill running" role="status"><span class="dot live" aria-hidden="true"></span>{runningOps} running</span>
  {/if}
  <span class="pill docker" role="group" aria-label={dockerText}>
    <span class="dot" class:ok={dockerOk} class:bad={!dockerOk} aria-hidden="true"></span><span class="label">{dockerText}</span>
  </span>
  {#if $live.compose !== 'unknown'}<span class="pill compose">{composeText}</span>{/if}
</header>

<style>
  .topbar {
    height: 44px;
    flex: none;
    box-sizing: border-box;
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 0 16px;
    border-bottom: 1px solid var(--border);
    background: var(--surface);
    position: sticky;
    top: 0;
    z-index: 5;
  }
  .crumbs { display: flex; align-items: center; gap: 6px; min-width: 0; font-size: 13px; }
  .crumb { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  a.crumb { color: var(--muted); text-decoration: none; }
  a.crumb:hover { color: var(--text); }
  .current { font-weight: 500; color: var(--text); }
  .mono { font-family: var(--mono); }
  .sep { color: var(--faint); }
  .spacer { flex: 1; }
  .pill {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    height: 24px;
    padding: 0 8px;
    box-sizing: border-box;
    border-radius: var(--radius-pill);
    border: 1px solid var(--border);
    color: var(--muted);
    font-size: 12px;
    white-space: nowrap;
  }
  .running { background: var(--info-soft); color: var(--info); border-color: transparent; }
  .dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; flex: none; }
  .dot.ok { background: var(--success); }
  .dot.bad { background: var(--danger); }
  @media (max-width: 900px) {
    .compose { display: none; }
    .docker .label { display: none; }
    .docker { padding: 0 7px; }
  }
  @media (max-width: 720px) {
    .crumbs > :not(:last-child) { display: none; }
    .topbar { padding: 0 12px; }
  }
</style>
