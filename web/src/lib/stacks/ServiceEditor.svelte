<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { api, ApiError } from '../api/client';
  import type { Operation, ServiceDetail, StackDetail } from '../api/types';
  import Banner from '../components/Banner.svelte';
  import Button from '../components/Button.svelte';
  import { toasts } from '../stores/toasts';
  import { refreshStatus } from '../stores/live';
  import { refreshRunningOps, refreshStacks } from '../stores/stacks';
  import type { ServiceForm as Form } from '../compose/form';
  import DiffView from './DiffView.svelte';
  import FileBrowser from './FileBrowser.svelte';
  import NetworkPicker from './NetworkPicker.svelte';
  import OperationPanel from './OperationPanel.svelte';
  import ServiceForm from './ServiceForm.svelte';
  import { createServiceEdit } from './serviceEdit';

  let {
    stack,
    service,
    onsaved,
    onapplied,
    onRawEditor,
    onreload,
    lifecycleDisabledReason,
    dirty = $bindable(false),
    ondirtychange,
  }: {
    stack: StackDetail;
    service: ServiceDetail;
    /** Called with the new compose hash after a successful save. */
    onsaved?: (hash: string) => void;
    /** Called when Apply has started an operation (the panel is shown here). */
    onapplied?: (op: Operation) => void;
    /** Send the user to the raw YAML editor (their form values stay untouched). */
    onRawEditor: () => void;
    /**
     * Re-fetch the stack after a conflict or when the file moved on. The parent keys this editor by
     * stack and service and must NOT remount it on reload: it just returns the fresh detail, and the
     * editor lays the user's changed groups on top of it.
     */
    onreload: () => Promise<StackDetail>;
    /** Why Apply (compose up) is unavailable, if it is. */
    lifecycleDisabledReason?: string;
    /** True while the form differs from the saved config; bind it to guard tab switches. */
    dirty?: boolean;
    ondirtychange?: (dirty: boolean) => void;
  } = $props();

  // The editor is keyed by stack and service in its parent; both are fixed for its lifetime.
  /* svelte-ignore state_referenced_locally */
  const stackName = stack.name;
  // A 503 means Docker went away: have the status banners catch up.
  function unavailableRefresh(e: unknown): never {
    if (e instanceof ApiError && e.status === 503) void refreshStatus();
    throw e;
  }
  /* svelte-ignore state_referenced_locally */
  const serviceName = service.name;
  /* svelte-ignore state_referenced_locally */
  const edit = createServiceEdit(
    serviceName,
    { config: service.config, readOnly: service.readOnly, hash: stack.hash },
    {
      preview: (config, hash) => api.previewService(stackName, serviceName, config, hash).catch(unavailableRefresh),
      update: async (config, hash) => {
        const res = await api.updateService(stackName, serviceName, config, hash).catch(unavailableRefresh);
        return { hash: res.data.hash, checksPartial: res.checksPartial };
      },
      compose: () => api.compose(stackName),
      reloadDetail: () => onreload(),
    },
  );
  const { errors, phase, notice, conflictMessage, previewText, checksPartial, rebased, discardedGroups, formVersion } = edit;
  const isDirty = edit.dirty;

  // ServiceForm mutates its form in place, so a local reactive copy feeds it and the state machine
  // receives snapshots of it.
  let form = $state<Form>(structuredFormCopy());
  function structuredFormCopy(): Form {
    const f = getStore(edit.form);
    return { ...f, readOnly: new Set(f.readOnly) };
  }
  function getStore<T>(s: { subscribe: (cb: (v: T) => void) => () => void }): T {
    let v!: T;
    s.subscribe((x) => (v = x))();
    return v;
  }
  $effect(() => {
    const snap = $state.snapshot(form) as Form;
    edit.form.set({ ...snap, readOnly: new Set(form.readOnly) });
  });
  function reseedForm() {
    form = structuredFormCopy();
  }
  // The machine replaces the form on rebase/discard (new read-only set, new values): follow it.
  let seenVersion = 0;
  $effect(() => {
    const v = $formVersion;
    if (v !== seenVersion) {
      seenVersion = v;
      reseedForm();
    }
  });

  let root: HTMLElement | undefined = $state();
  let serviceForm: ServiceForm | undefined = $state();
  let previewHeading: HTMLElement | undefined = $state();
  let conflictBox: HTMLElement | undefined = $state();
  let savedHeading: HTMLElement | undefined = $state();
  let browse: { mode: 'dir' | 'file'; cb: (path: string) => void } | undefined = $state();
  let operationId: string | undefined = $state();
  let applying = $state(false);
  let operationRunning = $state(false);

  $effect(() => {
    dirty = $isDirty;
    ondirtychange?.($isDirty);
  });

  const announcement = $derived(
    $phase === 'previewing'
      ? 'Preview ready. Review the changes, then save.'
      : $phase === 'saving'
        ? 'Saving…'
        : $phase === 'saved'
          ? 'Saved. You can apply the change now.'
          : $phase === 'conflict'
            ? 'Save refused: the file changed.'
            : '',
  );

  onMount(() => {
    const guard = (e: BeforeUnloadEvent) => {
      if ($isDirty && $phase !== 'saved') {
        e.preventDefault();
        e.returnValue = '';
      }
    };
    window.addEventListener('beforeunload', guard);
    return () => window.removeEventListener('beforeunload', guard);
  });

  const firstField = () => root?.querySelector<HTMLElement>('input, select, textarea');

  async function preview() {
    await edit.preview();
    await tick();
    if ($phase === 'previewing') previewHeading?.focus();
    else if ($phase === 'conflict') conflictBox?.focus();
    else if (Object.keys($errors).length > 0 || $notice) {
      serviceForm?.focusFirstError();
      if (!document.activeElement || document.activeElement === document.body) firstField()?.focus();
    }
  }

  async function save() {
    const ok = await edit.save();
    await tick();
    if (ok) {
      toasts.push({ kind: 'success', message: `Saved ${serviceName}. Apply to run the change.` });
      if ($checksPartial) {
        toasts.push({ kind: 'warning', message: 'Port and IP checks were partial (Docker unreachable)' });
      }
      onsaved?.(getStore(edit.hash));
      await tick();
      savedHeading?.focus();
    } else if ($phase === 'conflict') {
      conflictBox?.focus();
    } else {
      serviceForm?.focusFirstError();
    }
  }

  async function backToEditing() {
    edit.cancelPreview();
    await tick();
    firstField()?.focus();
  }

  async function reload(keep: boolean) {
    let detail: StackDetail;
    try {
      detail = await onreload();
    } catch (e) {
      toasts.push({ kind: 'danger', message: `Could not reload the stack: ${e instanceof Error ? e.message : String(e)}` });
      return;
    }
    if (keep) {
      edit.reload(detail);
      await preview(); // show the user's values against the new file
    } else {
      edit.discard(detail);
      reseedForm();
      await tick();
      firstField()?.focus();
    }
  }

  async function apply() {
    if (applying || operationRunning || lifecycleDisabledReason) return;
    applying = true;
    try {
      const op = await api.act(stackName, 'up');
      operationId = op.id;
      operationRunning = true;
      onapplied?.(op);
      void refreshRunningOps();
    } catch (e) {
      const msg = e instanceof ApiError || e instanceof Error ? e.message : String(e);
      toasts.push({ kind: 'danger', message: `Could not start apply: ${msg}` });
    } finally {
      applying = false;
    }
  }

  async function cancelOperation(id: string) {
    try {
      await api.cancel(id);
    } catch (e) {
      toasts.push({ kind: 'danger', message: `Could not cancel: ${e instanceof Error ? e.message : String(e)}` });
    }
  }

  function finished() {
    operationRunning = false;
    void refreshStacks();
    refreshRunningOps();
  }
</script>

{#snippet networkPicker(f: Form, rowErrors: Record<number, string>)}
  <NetworkPicker
    idPrefix="svc-networks"
    value={f.networks}
    onchange={(v) => (form.networks = v)}
    errors={rowErrors}
  />
{/snippet}

<div class="editor" bind:this={root}>
  <div class="visually-hidden" role="status" aria-live="polite">{announcement}</div>

  {#if $notice}
    <Banner kind="danger" title={$notice.kind === 'rawEditor' ? 'The form cannot make this change' : 'Could not complete the request'}>
      <span class="msg">{$notice.message}</span>
      {#if $notice.kind === 'rawEditor'}
        <div class="row">
          <Button variant="secondary" onclick={() => onRawEditor()}>Open raw YAML editor</Button>
        </div>
      {/if}
    </Banner>
  {/if}

  {#if $phase === 'conflict'}
    <div bind:this={conflictBox} tabindex="-1">
      <Banner kind="danger" title="Save refused">
        <span class="msg">{$conflictMessage}</span>
      </Banner>
      <div class="row">
        <Button variant="primary" onclick={() => reload(true)}>Reload</Button>
        <Button variant="secondary" onclick={() => reload(false)}>Discard my changes</Button>
      </div>
    </div>
  {/if}

  {#if $phase === 'previewing' || $phase === 'saving'}
    <h3 tabindex="-1" bind:this={previewHeading}>Review changes to {serviceName}</h3>
    {#if $rebased}
      <Banner kind="warning" title="The file changed since you opened this editor">
        Your changes were applied on top of the new version; review the diff.
        {#if $discardedGroups.length}
          Your changes to {$discardedGroups.join(', ')} were discarded because that part of the file can now only be
          edited as raw YAML.
        {/if}
      </Banner>
    {/if}
    <p class="hint">The compose file as it is now, against the file this edit would write.</p>
    {#if $previewText}
      <DiffView original={$previewText.original} modified={$previewText.modified} ariaLabel="Changes to compose.yaml" />
    {/if}
  {/if}

  <!-- Kept mounted (hidden) while previewing so focus, scroll and the form values survive. -->
  <div hidden={$phase === 'previewing' || $phase === 'saving'} inert={$phase === 'busy'}>
    <ServiceForm
      bind:this={serviceForm}
      bind:form
      errors={$errors}
      onbrowse={(mode, cb) => (browse = { mode, cb })}
      onRawEditor={() => onRawEditor()}
      {networkPicker}
    />
  </div>

  <div class="row">
    {#if $phase === 'previewing' || $phase === 'saving'}
      <Button variant="secondary" disabled={$phase === 'saving'} onclick={backToEditing}>Back to editing</Button>
      <Button variant="primary" loading={$phase === 'saving'} onclick={save}>Save</Button>
    {:else}
      <Button
        variant="primary"
        loading={$phase === 'busy'}
        disabledReason={$phase === 'conflict'
          ? 'Resolve the conflict first (Reload or Discard)'
          : !$isDirty
            ? 'No changes to preview'
            : undefined}
        onclick={preview}>Preview changes</Button
      >
    {/if}
  </div>

  {#if $phase === 'saved'}
    <section class="saved" aria-labelledby="saved-title">
      <h3 id="saved-title" tabindex="-1" bind:this={savedHeading}>Saved</h3>
      <p class="hint">The file is updated. Apply runs <code>compose up</code> so the containers match it.</p>
      <Button variant="primary" loading={applying} disabledReason={lifecycleDisabledReason ?? (operationRunning ? 'An operation is already running' : undefined)} onclick={apply}>Apply</Button>
    </section>
  {/if}

  {#if operationId}
    {#key operationId}
      <OperationPanel operationId={operationId} oncancel={cancelOperation} onfinished={finished} />
    {/key}
  {/if}
</div>

<FileBrowser
  open={browse !== undefined}
  mode={browse?.mode ?? 'dir'}
  title={browse?.mode === 'file' ? 'Choose a Dockerfile' : 'Choose a build context'}
  onpick={(path) => {
    browse?.cb(path);
    browse = undefined;
  }}
  onclose={() => (browse = undefined)}
/>

<style>
  .editor { display: flex; flex-direction: column; gap: var(--space-3); min-width: 0; }
  .row { display: flex; flex-wrap: wrap; gap: var(--space-2); margin-top: var(--space-2); }
  .hint { margin: 0 0 var(--space-2); color: var(--muted); }
  .msg { overflow-wrap: anywhere; white-space: pre-wrap; }
  h3 { margin: 0; }
  [hidden] { display: none; }
</style>
