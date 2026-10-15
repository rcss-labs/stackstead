<script lang="ts">
  import type { Alert } from '$lib/api/types';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import { link } from '$lib/router';

  let { alerts }: { alerts: Alert[] | undefined } = $props();
</script>

<section class="card">
  <h3>Alerts{#if alerts}{" "}<span class="count mono">{alerts.length}</span>{/if}</h3>
  {#if !alerts}
    <!-- not loaded yet or the fetch failed: say nothing rather than claim there are none -->
  {:else if alerts.length === 0}
    <p class="empty">No alerts.</p>
  {:else}
    <ul>
      {#each alerts as a, i (`${a.kind}|${a.stack ?? ''}|${a.container ?? ''}|${a.since}|${i}`)}
        <li>
          <i class="dot {a.severity}" aria-hidden="true"></i><span class="visually-hidden">{a.severity}</span>
          <div class="body">
            <div class="top">
              {#if a.stack}<a class="stack mono" href={`/stacks/${encodeURIComponent(a.stack)}`} use:link>{a.stack}</a>
              {:else if a.container}<a class="stack mono" href={`/containers/${encodeURIComponent(a.container)}`} use:link>{a.container}</a>{/if}
              <span class="kind {a.severity}">{a.kind}</span>
            </div>
            <div class="msg">{a.message}</div>
          </div>
          <span class="since"><RelativeTime iso={a.since} /></span>
        </li>
      {/each}
    </ul>
  {/if}
</section>

<style>
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); padding: 10px 12px; min-width: 0; }
  h3 { margin: 0 0 8px; font-size: 13px; font-weight: 600; }
  .count { color: var(--warning); font-weight: 500; margin-left: 4px; }
  .mono { font-family: var(--mono); }
  .empty { margin: 0; color: var(--muted); font-size: 12.5px; }
  ul { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 8px; }
  li { display: flex; align-items: flex-start; gap: 8px; min-width: 0; }
  .dot { flex: none; width: 7px; height: 7px; margin-top: 5px; border-radius: 50%; background: var(--info); }
  .dot.danger { background: var(--danger); }
  .dot.warning { background: var(--warning); }
  .body { flex: 1; min-width: 0; }
  .top { display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px; }
  .stack { font-size: 12.5px; color: var(--text); text-decoration: none; }
  .stack:hover { color: var(--accent); }
  .kind { font-size: 11px; text-transform: uppercase; color: var(--info); }
  .kind.danger { color: var(--danger); }
  .kind.warning { color: var(--warning); }
  .msg { color: var(--muted); font-size: 12px; overflow-wrap: anywhere; }
  .since { flex: none; color: var(--faint); font-size: 11px; }
</style>
