<script lang="ts">
  import type { Snippet } from 'svelte';

  let {
    label,
    id,
    error,
    hint,
    readonly = false,
    readonlyReason,
    children,
  }: {
    label: string;
    id: string;
    error?: string;
    hint?: string;
    readonly?: boolean;
    readonlyReason?: string;
    children?: Snippet;
  } = $props();

  let wrapper = $state<HTMLElement>();

  const errorId = $derived(`${id}-error`);
  const hintId = $derived(`${id}-hint`);
  const reasonId = $derived(`${id}-readonly`);

  // The control lives in the caller's snippet, so wire its ARIA state from here.
  $effect(() => {
    const control = wrapper?.querySelector<HTMLElement>(`[id="${CSS.escape(id)}"]`);
    if (!control) return;
    const described = [
      error ? errorId : '',
      hint ? hintId : '',
      readonly && readonlyReason ? reasonId : '',
    ].filter(Boolean);
    if (described.length) control.setAttribute('aria-describedby', described.join(' '));
    else control.removeAttribute('aria-describedby');
    if (error) control.setAttribute('aria-invalid', 'true');
    else control.removeAttribute('aria-invalid');
    if (readonly) {
      control.setAttribute('aria-readonly', 'true');
      if (control instanceof HTMLInputElement || control instanceof HTMLTextAreaElement) control.readOnly = true;
      else control.setAttribute('aria-disabled', 'true');
    } else {
      control.removeAttribute('aria-readonly');
      if (control instanceof HTMLInputElement || control instanceof HTMLTextAreaElement) control.readOnly = false;
      else control.removeAttribute('aria-disabled');
    }
  });
</script>

<div class="field" bind:this={wrapper}>
  <label for={id}>{label}</label>
  {@render children?.()}
  {#if hint}<p id={hintId} class="hint">{hint}</p>{/if}
  {#if readonly && readonlyReason}<p id={reasonId} class="hint">{readonlyReason}</p>{/if}
  {#if error}<p id={errorId} class="error"><span aria-hidden="true">✖ </span>{error}</p>{/if}
</div>

<style>
  .field { display: flex; flex-direction: column; gap: var(--space-1); }
  label { font-weight: 600; }
  .hint { color: var(--muted); font-size: 0.875rem; }
  .error { color: var(--danger); font-size: 0.875rem; overflow-wrap: anywhere; }
  .field :global(input),
  .field :global(select),
  .field :global(textarea) {
    padding: var(--space-2) var(--space-3);
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
  }
  .field :global([aria-invalid='true']) { border-color: var(--danger); }
  .field :global([aria-readonly='true']) { background: var(--surface-raised); color: var(--muted); }
</style>
