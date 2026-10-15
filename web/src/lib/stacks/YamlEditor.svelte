<script lang="ts">
  import { onMount } from 'svelte';
  import type { EditorHandle } from './cm';

  let {
    value = $bindable(''),
    readonly = false,
    ariaLabel,
    original,
    onchange,
  }: {
    value?: string;
    readonly?: boolean;
    ariaLabel: string;
    /** Baseline for the changed-line marks; none are drawn when absent. */
    original?: string;
    onchange?: (v: string) => void;
  } = $props();

  let host: HTMLDivElement | undefined = $state();
  let textarea: HTMLTextAreaElement | undefined = $state();
  let handle: EditorHandle | undefined = $state();
  let destroyed = false;

  function update(v: string) {
    value = v;
    onchange?.(v);
  }

  onMount(() => {
    // Dynamic import: CodeMirror is its own chunk. Until it arrives (or if it
    // fails to load) the textarea below is a fully working editor.
    import('./cm')
      .then(({ createEditor }) => {
        if (destroyed || !host) return;
        const hadFocus = textarea !== undefined && document.activeElement === textarea;
        handle = createEditor({ parent: host, value, readonly, ariaLabel, original, onchange: update });
        if (hadFocus) handle.focus(); // the textarea is about to be removed; do not drop the user's focus
      })
      .catch(() => {});
    return () => {
      destroyed = true;
      handle?.destroy();
      handle = undefined;
    };
  });

  // Keep CodeMirror in step with value changes made by the parent (reload, discard).
  $effect(() => {
    const v = value;
    if (handle && handle.getValue() !== v) handle.setValue(v);
  });

  $effect(() => {
    const o = original;
    handle?.setOriginal(o);
  });

  $effect(() => {
    const ro = readonly;
    handle?.setReadonly(ro);
  });

  /** 1-based line; best effort. */
  export function goToLine(line: number): void {
    if (handle) return handle.goToLine(line);
    if (!textarea) return;
    const lines = textarea.value.split('\n');
    const n = Math.min(Math.max(1, line), lines.length);
    const pos = lines.slice(0, n - 1).reduce((a, l) => a + l.length + 1, 0);
    textarea.focus();
    textarea.setSelectionRange(pos, pos);
  }
</script>

<div class="yaml-editor" bind:this={host}>
  {#if !handle}
    <textarea
      bind:this={textarea}
      class="fallback"
      aria-label={ariaLabel}
      {readonly}
      spellcheck="false"
      rows="20"
      {value}
      oninput={(e) => update(e.currentTarget.value)}
    ></textarea>
  {/if}
</div>

<style>
  .yaml-editor { min-width: 0; }
  .fallback {
    width: 100%;
    box-sizing: border-box;
    font-family: var(--mono);
    font-size: 0.875rem;
    padding: var(--space-3);
    border: 0;
    background: var(--surface);
    color: var(--text);
    resize: vertical;
  }
  .fallback:focus-visible { outline-offset: -2px; }
  .yaml-editor :global(.cm-editor) { max-height: 70vh; }
  .yaml-editor :global(.cm-scroller) { overflow: auto; }
</style>
