<script lang="ts">
  import type { Snippet } from 'svelte';

  let { title, description, children }: { title: string; description?: string; children?: Snippet } = $props();
</script>

<div class="empty">
  <h2>{title}</h2>
  {#if description}<p>{description}</p>{/if}
  {@render children?.()}
</div>

<style>
  .empty {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: var(--space-3);
    padding: 32px 16px;
    text-align: center;
    border: 1px dashed var(--border-strong);
    border-radius: var(--radius-md);
  }
  h2 { font-size: 14px; font-weight: 600; }
  p { color: var(--muted); }
</style>
