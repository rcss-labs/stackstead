<script lang="ts">
  import type { ServiceDetail } from '$lib/api/types';
  import Button from '$lib/components/Button.svelte';
  import { link } from '$lib/router';
  import { serviceState } from './summary';

  let {
    stack,
    service,
    onedit,
    logsHref,
    onlogs,
  }: {
    stack: string;
    service: ServiceDetail;
    onedit: () => void;
    logsHref: string;
    /** When set, a plain click on Logs calls this instead of navigating (the page guards unsaved edits). */
    onlogs?: () => void;
  } = $props();

  const state = $derived(serviceState(service));
  const image = $derived(service.config.image ?? (service.config.build ? `build: ${service.config.build.context}` : ''));
  const limits = $derived(
    [service.config.limits.cpus ? `${service.config.limits.cpus} CPU` : '', service.config.limits.memory ?? ''].filter(Boolean).join(' · '),
  );
  const rows = $derived([
    { label: 'Image', value: image },
    { label: 'Ports', value: (service.config.ports ?? []).join(', ') },
    { label: 'Volumes', value: (service.config.volumes ?? []).join(', ') },
    { label: 'Restart', value: service.config.restart ?? '' },
    { label: 'Limits', value: limits },
  ]);

  function logsClick(ev: MouseEvent) {
    if (!onlogs || ev.defaultPrevented || ev.button !== 0 || ev.ctrlKey || ev.metaKey || ev.shiftKey || ev.altKey) return;
    ev.preventDefault();
    onlogs();
  }
</script>

<article class="card" aria-label={service.name}>
  <header>
    <span class="dot {state.tone}" aria-hidden="true"></span>
    <h2>{service.name}</h2>
    <span class="state">{state.text}</span>
    <span class="actions">
      {#if onlogs}
        <a class="logs" href={logsHref} onclick={logsClick}>Logs</a>
      {:else}
        <a class="logs" href={logsHref} use:link>Logs</a>
      {/if}
      <Button variant="secondary" size="sm" aria-label="Edit {service.name}" onclick={onedit}>Edit</Button>
    </span>
  </header>
  <dl>
    {#each rows as row (row.label)}
      <dt>{row.label}</dt>
      <dd class:dash={!row.value}>{row.value || '—'}</dd>
    {/each}
  </dl>
  {#if service.readOnly.length > 0}
    <p class="muted">Read-only in the form: {service.readOnly.join(', ')}. Change these in the Compose tab.</p>
  {/if}
  {#if service.containers.length > 0}
    <ul class="containers">
      {#each service.containers as c (c.id)}
        <li><span class="mono">{c.name}</span> <span class="muted">{c.state}</span> <span class="muted">{c.status}</span></li>
      {/each}
    </ul>
  {/if}
</article>

<style>
  .card { border: 1px solid var(--border); border-radius: var(--radius-md); background: var(--surface); padding: 14px; }
  header { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  h2 { margin: 0; font-family: var(--mono); font-size: 14px; font-weight: 600; overflow-wrap: anywhere; }
  .dot { width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--faint); }
  .dot.success { background: var(--success); }
  .dot.warning { background: var(--warning); }
  .dot.danger { background: var(--danger); }
  .state { color: var(--muted); font-size: 12px; min-width: 0; overflow-wrap: anywhere; }
  .actions { margin-inline-start: auto; display: inline-flex; gap: 6px; align-items: center; }
  .logs {
    display: inline-flex; align-items: center; height: 26px; padding: 0 10px; font-size: 12px;
    border: 1px solid var(--border); border-radius: var(--radius); color: var(--muted); text-decoration: none;
  }
  .logs:hover { background: var(--hover); color: var(--text); }
  dl { display: grid; grid-template-columns: 80px minmax(0, 1fr); gap: 6px 12px; margin: 12px 0 0; font-size: 12.5px; }
  dt { color: var(--muted); }
  dd { margin: 0; font-family: var(--mono); overflow-wrap: anywhere; }
  dd.dash { color: var(--faint); }
  .muted { color: var(--muted); }
  p.muted { margin: 10px 0 0; font-size: 12.5px; }
  .containers { list-style: none; padding: 10px 0 0; margin: 10px 0 0; border-top: 1px solid var(--border); display: flex; flex-direction: column; gap: 4px; font-size: 12.5px; }
  .mono { font-family: var(--mono); }
</style>
