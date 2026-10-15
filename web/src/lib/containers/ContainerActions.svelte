<script lang="ts">
  import { api, ApiError } from '$lib/api/client';
  import type { ContainerRow } from '$lib/api/types';
  import Button from '$lib/components/Button.svelte';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';
  import { link } from '$lib/router';
  import { ICONS } from '$lib/shell/icons';
  import { BUSY_REASON } from '$lib/stacks/lifecycle';
  import { live, refreshStatus } from '$lib/stores/live';
  import { toasts } from '$lib/stores/toasts';

  type ContainerAction = 'start' | 'stop' | 'restart';

  let {
    row,
    size = 'icon',
    onchanged,
  }: {
    row: ContainerRow;
    /** `icon`: 26 px icon buttons (table rows); `md`: labelled buttons (page header). */
    size?: 'icon' | 'md';
    onchanged?: (row: ContainerRow) => void;
  } = $props();

  const doneText: Record<ContainerAction, string> = { start: 'started', stop: 'stopped', restart: 'restarted' };
  const labelOf = (a: ContainerAction) => a[0].toUpperCase() + a.slice(1);
  const iconOf: Record<ContainerAction, 'play' | 'stop' | 'restart'> = { start: 'play', stop: 'stop', restart: 'restart' };

  const running = $derived(row.state === 'running');
  const actions = $derived<ContainerAction[]>(running ? ['stop', 'restart'] : ['start', 'restart']);

  let inFlight = $state(false);
  let confirming = $state<ContainerAction | undefined>();

  const reason = $derived(
    $live.docker === 'unavailable' || $live.events === 'disconnected' ? 'Docker is unreachable' : inFlight ? BUSY_REASON : undefined,
  );

  function request(action: ContainerAction) {
    if (action === 'start') void run(action);
    else confirming = action;
  }

  async function run(action: ContainerAction) {
    confirming = undefined;
    if (inFlight) return;
    inFlight = true;
    try {
      const fresh = await api.containerAction(row.id, action);
      toasts.push({ kind: 'success', message: `${row.name} ${doneText[action]}` });
      onchanged?.(fresh);
    } catch (e) {
      if (e instanceof ApiError && e.status === 503) void refreshStatus();
      toasts.push({ kind: e instanceof ApiError && e.status === 409 ? 'warning' : 'danger', message: e instanceof Error ? e.message : String(e) });
    } finally {
      inFlight = false;
    }
  }
</script>

{#if row.managed === 'stack'}
  <a class="manage" href={`/stacks/${encodeURIComponent(row.stack)}`} use:link>Manage in stack →</a>
{:else}
  <span class="btns">
    {#each actions as action (action)}
      {@const label = `${labelOf(action)} ${row.name}`}
      <Button variant="ghost" data-action={action} size={size === 'icon' ? 'sm' : 'md'} icon={size === 'icon'} aria-label={label} title={label} disabledReason={reason} onclick={() => request(action)}>
        {#snippet children()}
          <svg width={size === 'icon' ? 14 : 16} height={size === 'icon' ? 14 : 16} viewBox="0 0 24 24" fill={action === 'restart' ? 'none' : 'currentColor'} stroke={action === 'restart' ? 'currentColor' : 'none'} stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d={ICONS[iconOf[action]]}></path></svg>
          {#if size === 'md'}{labelOf(action)}{/if}
        {/snippet}
      </Button>
    {/each}
  </span>
{/if}

{#if confirming}
<ConfirmDialog
  open
  title={`${labelOf(confirming)} ${row.name}?`}
  message={confirming === 'stop'
    ? `Stop container ${row.name}? Anything it serves goes offline until it is started again.`
    : `Restart container ${row.name}? It is briefly unavailable while it restarts.`}
  confirmLabel={labelOf(confirming)}
  danger
  onconfirm={() => confirming && void run(confirming)}
  oncancel={() => (confirming = undefined)}
/>
{/if}

<style>
  .btns { display: inline-flex; gap: var(--space-1); }
  .btns :global(button[data-action='start']:hover) { color: var(--success); }
  .btns :global(button[data-action='stop']:hover) { color: var(--danger); }
  .manage { font-size: 12px; color: var(--accent); text-decoration: none; white-space: nowrap; }
  .manage:hover { text-decoration: underline; }
</style>
