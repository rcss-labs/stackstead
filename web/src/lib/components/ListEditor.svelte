<script lang="ts" generics="T">
  import { tick, type Snippet } from 'svelte';

  let {
    items,
    onchange,
    addLabel,
    newItem,
    row,
    readonly = false,
    errors = {},
    title,
    hint,
  }: {
    items: T[];
    onchange: (items: T[]) => void;
    addLabel: string;
    /** Value appended by the add button. */
    newItem: () => T;
    row: Snippet<[T, number, (value: T) => void]>;
    readonly?: boolean;
    errors?: Record<number, string>;
    /** Group heading shown left of the add button. */
    title?: string;
    hint?: string;
  } = $props();

  const uid = $props.id();
  const FIELD = 'input, select, textarea, button:not([data-remove])';
  let list = $state<HTMLElement>();
  let addButton = $state<HTMLButtonElement>();
  // Where focus goes once the parent has applied the change.
  let pendingFocus: { row: number } | 'add' | null = null;

  $effect(() => {
    void items.length;
    if (!pendingFocus) return;
    const target = pendingFocus;
    pendingFocus = null;
    if (target === 'add') {
      addButton?.focus();
      return;
    }
    list?.querySelectorAll<HTMLElement>(':scope > li')[target.row]?.querySelector<HTMLElement>(FIELD)?.focus();
  });

  // If the parent ignored the change the length never moves; drop the request so it
  // cannot steal focus from some later, unrelated update.
  async function change(next: T[], focus: typeof pendingFocus) {
    pendingFocus = focus;
    onchange(next);
    await tick();
    pendingFocus = null;
  }

  function add() {
    return change([...items, newItem()], { row: items.length });
  }

  function remove(index: number) {
    const remaining = items.length - 1;
    return change(
      items.filter((_, i) => i !== index),
      remaining === 0 ? 'add' : { row: Math.min(Math.max(index - 1, 0), remaining - 1) },
    );
  }

  function update(index: number, value: T) {
    onchange(items.map((item, i) => (i === index ? value : item)));
  }
</script>

<div class="editor">
  {#if title || !readonly}
    <div class="head">
      {#if title}<span class="title">{title}</span>{/if}
      {#if hint}<span class="hint">{hint}</span>{/if}
      {#if !readonly}
        <button type="button" class="add" aria-label={addLabel} bind:this={addButton} onclick={add}>+ Add</button>
      {/if}
    </div>
  {/if}
  <ul bind:this={list} class="rows">
    {#each items as item, i (i)}
      <li>
        <div class="row" role="group" aria-label="Item {i + 1}" aria-describedby={errors[i] ? `${uid}-err-${i}` : undefined}>
          <div class="cell">{@render row(item, i, (value: T) => update(i, value))}</div>
          {#if !readonly}
            <button type="button" data-remove aria-label="Remove item {i + 1}" onclick={() => remove(i)}>×</button>
          {/if}
        </div>
        {#if errors[i]}<p id="{uid}-err-{i}" class="error"><span aria-hidden="true">✖ </span>{errors[i]}</p>{/if}
      </li>
    {/each}
  </ul>
</div>

<style>
  .editor { display: flex; flex-direction: column; gap: 8px; }
  .head { display: flex; align-items: baseline; gap: 10px; min-height: 24px; }
  .title { font-size: 12.5px; font-weight: 600; }
  .hint { color: var(--faint); font-size: 11.5px; font-family: var(--mono); }
  .rows { list-style: none; padding: 0; width: 100%; display: flex; flex-direction: column; gap: 8px; }
  .row { display: flex; gap: 8px; align-items: center; }
  .cell { flex: 1; min-width: 0; display: flex; gap: 8px; }
  .cell > :global(input) {
    flex: 1;
    min-width: 0;
    height: 32px;
    padding: 0 10px;
    background: var(--bg);
    color: var(--text);
    border: 1px solid var(--border-input);
    border-radius: var(--radius);
    font-family: var(--mono);
    font-size: 12.5px;
  }
  .cell > :global(input[aria-invalid='true']) { border-color: var(--danger); }
  .cell > :global(input:read-only) { background: var(--raised); color: var(--muted); }
  .error { color: var(--danger); font-size: 0.875rem; overflow-wrap: anywhere; }
  button {
    background: transparent;
    color: var(--muted);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    font: inherit;
    font-size: 12px;
    cursor: pointer;
  }
  button:hover { background: var(--hover); color: var(--text); }
  button[data-remove] { flex: none; width: 30px; height: 30px; font-size: 16px; line-height: 1; }
  .add { height: 24px; padding: 0 8px; margin-left: auto; align-self: center; }
</style>
