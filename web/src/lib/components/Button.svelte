<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    variant,
    loading = false,
    disabled = false,
    disabledReason,
    type = 'button',
    onclick,
    children,
  }: {
    variant: 'primary' | 'secondary' | 'danger' | 'ghost';
    loading?: boolean;
    disabled?: boolean;
    disabledReason?: string;
    type?: 'button' | 'submit' | 'reset';
    onclick?: (event: MouseEvent) => void;
    children?: Snippet;
  } = $props();

  const uid = $props.id();
  // aria-disabled keeps the button focusable so the reason can be discovered.
  const soft = $derived(Boolean(disabledReason) || loading);

  function handleClick(event: MouseEvent) {
    if (soft) {
      event.preventDefault();
      return;
    }
    onclick?.(event);
  }
</script>

<button
  class="btn {variant}"
  {type}
  disabled={disabled && !disabledReason}
  aria-disabled={soft ? 'true' : undefined}
  aria-busy={loading ? 'true' : undefined}
  aria-describedby={disabledReason ? `${uid}-reason` : undefined}
  title={disabledReason}
  onclick={handleClick}
>
  {#if loading}<span class="spin" aria-hidden="true"></span>{/if}
  {@render children?.()}
</button>
{#if disabledReason}<span id="{uid}-reason" class="visually-hidden">{disabledReason}</span>{/if}

<style>
  .btn {
    display: inline-flex;
    align-items: center;
    gap: var(--space-2);
    padding: var(--space-2) var(--space-4);
    min-height: 2.25rem;
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    background: var(--surface-raised);
    cursor: pointer;
  }
  .primary { background: var(--accent); border-color: var(--accent); color: var(--accent-contrast); }
  .danger { background: var(--danger); border-color: var(--danger); color: var(--surface); }
  .ghost { background: transparent; border-color: transparent; }
  .btn:disabled,
  .btn[aria-disabled='true'] { opacity: 0.6; cursor: not-allowed; }
  .btn[aria-busy='true'] { cursor: progress; }
  .spin {
    width: 1em;
    height: 1em;
    border: 2px solid currentColor;
    border-right-color: transparent;
    border-radius: 50%;
    animation: spin 0.8s linear infinite;
  }
  @keyframes spin { to { transform: rotate(360deg); } }
</style>
