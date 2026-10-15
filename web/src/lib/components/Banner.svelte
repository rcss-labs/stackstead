<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    kind,
    title,
    children,
    dismissible = false,
  }: { kind: 'info' | 'warning' | 'danger'; title: string; children?: Snippet; dismissible?: boolean } = $props();

  let dismissed = $state(false);
  const icon = { info: 'ℹ', warning: '⚠', danger: '✖' } as const;
</script>

{#if !dismissed}
  <div class="banner {kind}" role={kind === 'danger' ? 'alert' : 'status'}>
    <span class="icon" aria-hidden="true">{icon[kind]}</span>
    <div class="body">
      <strong>{title}</strong>
      {#if children}<div>{@render children()}</div>{/if}
    </div>
    {#if dismissible}
      <button type="button" class="close" onclick={() => (dismissed = true)}>Dismiss</button>
    {/if}
  </div>
{/if}

<style>
  .banner {
    display: flex;
    gap: var(--space-3);
    align-items: flex-start;
    padding: 10px 12px;
    border: 1px solid var(--border);
    border-left: 3px solid var(--tone);
    border-radius: var(--radius-md);
    background: var(--tone-soft);
    font-size: 12.5px;
  }
  strong { font-size: 13px; font-weight: 600; color: var(--text); }
  .body { color: var(--muted); }
  .info { --tone: var(--info); --tone-soft: var(--info-soft); }
  .warning { --tone: var(--warning); --tone-soft: var(--warning-soft); }
  .danger { --tone: var(--danger); --tone-soft: var(--danger-soft); }
  .icon { color: var(--tone); }
  .body { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  .close {
    background: none;
    color: var(--text);
    font: inherit;
    font-size: 12px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    cursor: pointer;
    padding: var(--space-1) var(--space-2);
  }
</style>
