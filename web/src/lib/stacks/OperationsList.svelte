<script lang="ts">
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import { formatDuration } from '$lib/format';
  import { link } from '$lib/router';
  import type { Operation } from '$lib/api/types';
  import { operationDurationMs, operationStateLook } from './operation';

  let {
    operations,
    selected,
    href,
  }: { operations: Operation[]; selected?: string; href: (id: string) => string } = $props();

  let now = $state(Date.now());
  const anyRunning = $derived(operations.some((o) => o.state === 'running'));

  // One shared ticker, alive only while a running row is rendered.
  $effect(() => {
    if (!anyRunning) return;
    now = Date.now();
    const timer = setInterval(() => (now = Date.now()), 1000);
    return () => clearInterval(timer);
  });

  function duration(op: Operation): string {
    const ms = operationDurationMs(op, now);
    return ms === undefined ? '' : formatDuration(ms);
  }
</script>

{#if operations.length === 0}
  <p class="empty">No operations yet.</p>
{:else}
  <ul class="list">
    {#each operations as o (o.id)}
      {@const look = operationStateLook(o.state)}
      <li>
        <a href={href(o.id)} use:link aria-current={o.id === selected ? 'true' : undefined} class="row">
          <span class="action">{o.action}</span>
          <span class="state {look.tone}"><span class="dot" aria-hidden="true"></span>{look.label}</span>
          <span class="cmd">docker compose {o.action}</span>
          <RelativeTime iso={o.startedAt} />
          <span class="duration">{duration(o)}</span>
        </a>
      </li>
    {/each}
  </ul>
{/if}

<style>
  .list { list-style: none; margin: 0; padding: 0; background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); overflow: hidden; }
  li + li { border-top: 1px solid var(--border); }
  .row {
    display: grid;
    grid-template-columns: 70px 100px minmax(0, 1fr) 110px 60px;
    gap: 12px;
    align-items: center;
    min-height: var(--row);
    padding: var(--pad) 12px;
    font-size: 12.5px;
    color: var(--text);
    text-decoration: none;
  }
  .row:hover { background: var(--hover); }
  .row[aria-current='true'] { background: var(--accent-soft); }
  .action { font-family: var(--mono); font-weight: 500; }
  .state { display: inline-flex; align-items: center; gap: 6px; font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.02em; color: var(--tone); white-space: nowrap; }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--tone); flex: none; }
  .success { --tone: var(--success); }
  .warning { --tone: var(--warning); }
  .danger { --tone: var(--danger); }
  .muted { --tone: var(--muted); }
  .cmd { color: var(--muted); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .duration { font-family: var(--mono); text-align: end; }
  .empty { color: var(--muted); margin: 0; }
</style>
