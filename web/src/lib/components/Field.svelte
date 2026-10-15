<script lang="ts">
  import type { Snippet } from 'svelte';

  /** What the caller spreads onto its control. */
  export interface FieldControlProps {
    id: string;
    'aria-describedby'?: string;
    'aria-invalid'?: 'true';
    /**
     * True when the field is read-only. Text inputs/textareas take `readonly={readonly}`;
     * selects, checkboxes and radios ignore `readonly`, so render them `disabled={readonly}`
     * (the visible readonlyReason explains why).
     */
    readonly: boolean;
  }

  let {
    label,
    id,
    error,
    hint,
    readonly = false,
    readonlyReason,
    mono = false,
    children,
  }: {
    label: string;
    id: string;
    error?: string;
    hint?: string;
    readonly?: boolean;
    readonlyReason?: string;
    mono?: boolean;
    children?: Snippet<[FieldControlProps]>;
  } = $props();

  const errorId = $derived(`${id}-error`);
  const hintId = $derived(`${id}-hint`);
  const reasonId = $derived(`${id}-readonly`);

  const control = $derived.by<FieldControlProps>(() => {
    const described = [
      error ? errorId : '',
      hint ? hintId : '',
      readonly && readonlyReason ? reasonId : '',
    ].filter(Boolean);
    return {
      id,
      'aria-describedby': described.length ? described.join(' ') : undefined,
      'aria-invalid': error ? 'true' : undefined,
      readonly,
    };
  });
</script>

<div class="field" class:mono>
  <label for={id}>{label}</label>
  {@render children?.(control)}
  {#if hint}<p id={hintId} class="hint">{hint}</p>{/if}
  {#if readonly && readonlyReason}<p id={reasonId} class="hint">{readonlyReason}</p>{/if}
  {#if error}<p id={errorId} class="error"><span aria-hidden="true">✖ </span>{error}</p>{/if}
</div>

<style>
  .field { display: flex; flex-direction: column; gap: var(--space-1); }
  label { font-size: 12px; font-weight: 500; color: var(--muted); }
  .hint { color: var(--faint); font-size: 11.5px; }
  .error { color: var(--danger); font-size: 12px; overflow-wrap: anywhere; }
  .field :global(input),
  .field :global(select),
  .field :global(textarea) {
    height: 32px;
    padding: 0 10px;
    background: var(--bg);
    color: var(--text);
    border: 1px solid var(--border-input);
    border-radius: var(--radius);
    font: inherit;
    font-size: 13px;
  }
  .field :global(textarea) { height: auto; padding-block: 6px; }
  .field.mono :global(input),
  .field.mono :global(select),
  .field.mono :global(textarea) { font-family: var(--mono); }
  .field :global([aria-invalid='true']) { border-color: var(--danger); }
  .field :global(:is(input, textarea):read-only),
  .field :global(:is(input, select, textarea):disabled) { background: var(--raised); color: var(--muted); }
</style>
