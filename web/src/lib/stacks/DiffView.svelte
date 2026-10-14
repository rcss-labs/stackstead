<script lang="ts">
  let { original, modified, ariaLabel }: { original: string; modified: string; ariaLabel: string } = $props();

  let host: HTMLDivElement | undefined = $state();
  let ready = $state(false);

  // The merge view is immutable once built, so rebuild it when either side changes.
  $effect(() => {
    const o = original;
    const m = modified;
    const el = host;
    if (!el) return;
    let cancelled = false;
    let view: { destroy(): void } | undefined;
    import('./cm')
      .then(({ createDiffView }) => {
        if (cancelled) return;
        view = createDiffView({ parent: el, original: o, modified: m, ariaLabel: `${ariaLabel} (unified diff)` });
        ready = true;
      })
      .catch(() => {});
    return () => {
      cancelled = true;
      view?.destroy();
      ready = false;
    };
  });
</script>

<div class="diff" role="group" aria-label={ariaLabel} data-testid="diff-view">
  <div bind:this={host}></div>
  {#if !ready}
    <!-- Plain-text fallback until CodeMirror loads: the proposed text, as text. -->
    <p class="note">Diff unavailable (loading); showing your draft.</p>
    <pre class="fallback">{modified}</pre>
  {/if}
</div>

<style>
  .diff { min-width: 0; }
  .note { margin: 0 0 var(--space-2); color: var(--muted); }
  .fallback {
    margin: 0;
    padding: var(--space-3);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    font-family: var(--font-mono);
    font-size: 0.875rem;
    overflow: auto;
    max-height: 70vh;
  }
  .diff :global(.cm-editor) { max-height: 70vh; }
  .diff :global(.cm-scroller) { overflow: auto; }
  .diff :global(.cm-deletedChunk) { background: color-mix(in srgb, var(--danger) 15%, transparent); }
  .diff :global(.cm-changedLine) { background: color-mix(in srgb, var(--success) 15%, transparent); }
</style>
