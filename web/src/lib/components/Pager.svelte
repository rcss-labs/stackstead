<script lang="ts">
  import Button from './Button.svelte';

  let { page, pages, from, to, total, label, onpage }: { page: number; pages: number; from: number; to: number; total: number; label: string; onpage: (page: number) => void } = $props();
</script>

{#if pages > 1}
  <nav class="pager" aria-label="{label} pages">
    <Button variant="secondary" size="sm" disabled={page <= 1} onclick={() => onpage(page - 1)}>Previous</Button>
    <span class="status muted" aria-live="polite">{from}–{to} of {total}</span>
    <Button variant="secondary" size="sm" disabled={page >= pages} onclick={() => onpage(page + 1)}>Next</Button>
  </nav>
{/if}

<style>
  .pager { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: var(--space-3); margin-top: var(--space-3); }
  .muted { color: var(--muted); font-size: 12px; }
</style>
