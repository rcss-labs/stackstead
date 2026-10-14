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
    padding: var(--space-6) var(--space-4);
    text-align: center;
    border: 1px dashed var(--border);
    border-radius: var(--radius-lg);
  }
  p { color: var(--muted); }
</style>
