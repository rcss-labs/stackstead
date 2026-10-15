<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    variant,
    size = 'md',
    icon = false,
    confirm = false,
    loading = false,
    disabled = false,
    disabledReason,
    type = 'button',
    onclick,
    children,
    ...rest
  }: {
    variant: 'primary' | 'secondary' | 'danger' | 'ghost';
    size?: 'md' | 'sm' | 'xs';
    icon?: boolean;
    /** Set by ConfirmDialog: the danger variant is then filled so the destructive action is visible. */
    confirm?: boolean;
    loading?: boolean;
    disabled?: boolean;
    disabledReason?: string;
    type?: 'button' | 'submit' | 'reset';
    onclick?: (event: MouseEvent) => void;
    children?: Snippet;
    [attr: string]: unknown;
  } = $props();

  const uid = $props.id();
  // A disabledReason (or loading) soft-disables: aria-disabled + click ignored, but still
  // focusable so the reason can be found. Without a reason, `disabled` is a real disabled button.
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
  {...rest}
  class="btn {variant} {size}"
  class:icon
  class:confirm
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
    justify-content: center;
    gap: var(--space-2);
    height: 30px;
    padding: 0 12px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius);
    background: var(--surface);
    color: var(--text);
    font: inherit;
    font-size: 13px;
    white-space: nowrap;
    cursor: pointer;
  }
  .btn.sm { height: 26px; padding: 0 10px; font-size: 12px; }
  .btn.xs { height: 24px; padding: 0 8px; font-size: 12px; }
  .btn.icon { width: 30px; padding: 0; }
  .btn.icon.sm { width: 26px; }
  .btn.icon.xs { width: 24px; }
  .btn:hover { background: var(--hover); }
  .primary { background: var(--accent); border-color: var(--accent); color: var(--accent-ink); font-weight: 600; }
  .primary:hover { background: var(--accent); filter: brightness(1.08); }
  .danger { background: transparent; border-color: var(--border); color: var(--danger); }
  .danger:hover,
  .danger.confirm { background: var(--danger-soft); }
  .ghost { background: transparent; border-color: var(--border); color: var(--muted); }
  .ghost:hover { background: var(--hover); color: var(--text); }
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
