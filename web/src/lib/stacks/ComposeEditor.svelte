<script lang="ts">
  import { onMount } from 'svelte';
  import { api } from '../api/client';
  import Banner from '../components/Banner.svelte';
  import Button from '../components/Button.svelte';
  import { toasts } from '../stores/toasts';
  import { createComposeEditor } from './compose';
  import DiffView from './DiffView.svelte';
  import YamlEditor from './YamlEditor.svelte';

  let {
    stackName,
    parseError,
    onsaved,
    onapply,
  }: { stackName: string; parseError?: string; onsaved?: (hash: string) => void; onapply?: () => void } = $props();

  // The component is keyed by stack in its parent; the name is fixed for its lifetime.
  // svelte-ignore state_referenced_locally
  const name = stackName;
  const editor = createComposeEditor(name, {
    load: () => api.compose(name),
    save: async (content, hash) => {
      const res = await api.saveCompose(name, content, hash);
      return { hash: res.data.hash, checksPartial: res.checksPartial };
    },
  });
  const st = editor.state;

  let yaml: YamlEditor | undefined = $state();
  const dirty = $derived($st.draft !== $st.content);
  const showEditor = $derived($st.phase !== 'previewing' && !$st.loadFailed);

  onMount(() => {
    void editor.reload();
    const guard = (e: BeforeUnloadEvent) => {
      if (dirty) e.preventDefault();
    };
    window.addEventListener('beforeunload', guard);
    return () => window.removeEventListener('beforeunload', guard);
  });

  async function save() {
    if (!dirty || $st.phase === 'saving' || $st.phase === 'loading') return;
    const ok = await editor.save();
    if (ok) {
      toasts.push({ kind: 'success', message: 'Saved — previous version kept as compose.yaml.bak' });
      if ($st.checksPartial) {
        toasts.push({ kind: 'warning', message: 'Port and IP checks were partial (Docker unreachable)' });
      }
      onsaved?.($st.hash);
    } else if ($st.phase === 'editing' && $st.error) {
      // Best effort: compose usually names the failing line.
      const m = /line (\d+)/.exec($st.error);
      if (m) queueMicrotask(() => yaml?.goToLine(Number(m[1])));
    }
  }

  function onkeydown(e: KeyboardEvent) {
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
      e.preventDefault();
      void save();
    }
  }
</script>

<!-- svelte-ignore a11y_no_static_element_interactions -->
<div class="compose" {onkeydown}>
  {#if parseError}
    <Banner kind="danger" title="This file cannot be edited with forms">
      Only the raw editor is available. <pre class="msg">{parseError}</pre>
    </Banner>
  {/if}

  {#if $st.phase === 'loading'}
    <p role="status">Loading compose file…</p>
  {:else if $st.loadFailed}
    <Banner kind="danger" title="Could not load the compose file">{$st.error}</Banner>
    <Button variant="secondary" onclick={() => editor.reload()}>Retry</Button>
  {/if}

  {#if $st.phase === 'conflict'}
    <Banner kind="danger" title="Save refused">
      <span class="msg-text">{$st.conflictMessage}</span>
    </Banner>
    <div class="row">
      <Button variant="primary" onclick={() => editor.reload({ keepDraft: true })}>Reload</Button>
      <Button variant="secondary" onclick={() => editor.reload({ keepDraft: false })}>Discard my changes</Button>
    </div>
    {#if $st.error}<pre class="msg">{$st.error}</pre>{/if}
  {/if}

  {#if $st.phase === 'previewing'}
    <p class="hint">Showing the file as it is now against your draft.</p>
    <DiffView original={$st.content} modified={$st.draft} ariaLabel="Changes to compose.yaml" />
  {/if}

  {#if $st.phase !== 'loading' && !$st.loadFailed}
    <!-- Kept mounted (hidden) during preview so the editor and its undo history survive. -->
    <div hidden={!showEditor}>
      <YamlEditor
        bind:this={yaml}
        value={$st.draft}
        ariaLabel="Compose file"
        readonly={$st.phase === 'saving'}
        onchange={(v) => editor.setDraft(v)}
      />
    </div>

    {#if $st.phase === 'editing' && $st.error}
      <pre class="msg error" data-testid="compose-error">{$st.error}</pre>
    {/if}

    <div class="row">
      {#if $st.phase === 'previewing'}
        <Button variant="secondary" onclick={() => editor.cancelPreview()}>Back to editing</Button>
      {:else}
        <Button
          variant="secondary"
          disabledReason={dirty ? undefined : 'No changes to preview'}
          onclick={() => editor.preview()}>Preview changes</Button
        >
      {/if}
      <Button
        variant="primary"
        loading={$st.phase === 'saving'}
        disabledReason={dirty ? undefined : 'No changes to save'}
        onclick={save}>Save</Button
      >
      {#if $st.phase === 'saved'}
        <Button variant="secondary" onclick={() => onapply?.()}>Apply</Button>
      {/if}
    </div>
  {/if}
</div>

<style>
  .compose { display: flex; flex-direction: column; gap: var(--space-3); min-width: 0; }
  .row { display: flex; flex-wrap: wrap; gap: var(--space-2); }
  .hint { margin: 0; color: var(--muted); }
  .msg {
    margin: var(--space-2) 0 0;
    white-space: pre-wrap;
    overflow-wrap: anywhere;
    font-family: var(--font-mono);
    font-size: 0.875rem;
  }
  .error { padding: var(--space-3); border: 1px solid var(--danger); border-radius: var(--radius-md); }
  [hidden] { display: none; }
</style>
