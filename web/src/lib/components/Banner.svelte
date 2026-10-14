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
    padding: var(--space-3) var(--space-4);
    border: 1px solid var(--tone);
    border-left-width: 4px;
    border-radius: var(--radius-md);
    background: var(--surface-raised);
  }
  .info { --tone: var(--info); }
  .warning { --tone: var(--warning); }
  .danger { --tone: var(--danger); }
  .icon { color: var(--tone); }
  .body { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  .close {
    background: none;
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    cursor: pointer;
    padding: var(--space-1) var(--space-2);
  }
</style>
