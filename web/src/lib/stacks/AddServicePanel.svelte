<script lang="ts">
  import { onDestroy, onMount, tick } from 'svelte';
  import { get } from 'svelte/store';
  import { api, ApiError } from '../api/client';
  import type { StackDetail } from '../api/types';
  import type { ServiceForm as Form } from '../compose/form';
  import Banner from '../components/Banner.svelte';
  import Button from '../components/Button.svelte';
  import { refreshStatus } from '../stores/live';
  import { refreshStacks } from '../stores/stacks';
  import { toasts } from '../stores/toasts';
  import DiffView from './DiffView.svelte';
  import FileBrowser from './FileBrowser.svelte';
  import NetworkPicker from './NetworkPicker.svelte';
  import ServiceForm from './ServiceForm.svelte';
  import { createServiceAdd } from './serviceAdd';
  let { stack, onadded, oncancel, onreload, dirty = $bindable(false), ondirtychange }: {
    stack: StackDetail;
    onadded: (detail: StackDetail, checksPartial: boolean) => void;
    oncancel: () => void;
    onreload: () => Promise<StackDetail>;
    dirty?: boolean;
    ondirtychange?: (dirty: boolean) => void;
  } = $props();
  /* svelte-ignore state_referenced_locally */
  const stackName = stack.name;
  let disposed = false;
  let savedService = '';
  let generation = 0;
  let reloading = $state(false);
  const available = (e: unknown): never => {
    if (e instanceof ApiError && e.status === 503)
      void refreshStatus();
    throw e;
  };
  /* svelte-ignore state_referenced_locally */
  const add = createServiceAdd(stack.hash, {
    preview: (service, config, hash) => api.previewAddService(stackName, service, config, hash).catch(available),
    add: async (service, config, hash) => {
      savedService = service;
      const res = await api.addService(stackName, service, config, hash).catch(available);
      // The server accepted the write even if this session has since been closed.
      void refreshStacks();
      return { detail: res.data, checksPartial: res.checksPartial };
    },
    compose: () => api.compose(stackName).catch(available), reloadDetail: () => onreload(),
  });
  const { errors, phase, notice, conflictMessage, previewText, checksPartial } = add;
  const isDirty = add.dirty;
  let form = $state<Form>(get(add.form));
  let name = $state('');
  let serviceForm: ServiceForm | undefined = $state();
  let heading: HTMLElement | undefined = $state();
  let browse: {
    mode: 'dir' | 'file';
    cb: (path: string) => void;
  } | undefined = $state();
  $effect(() => {
    add.form.set($state.snapshot(form) as Form);
    add.name.set(name);
  });
  $effect(() => {
    dirty = $isDirty;
    ondirtychange?.($isDirty);
  });
  onDestroy(() => {
    disposed = true;
    generation++;
    add.dispose();
  });
  onMount(() => {
    const guard = (e: BeforeUnloadEvent) => {
      if (get(isDirty)) {
        e.preventDefault();
        e.returnValue = '';
      }
    };
    window.addEventListener('beforeunload', guard);
    return () => window.removeEventListener('beforeunload', guard);
  });
  async function preview() {
    await add.preview();
    if (disposed)
      return;
    await tick();
    if ($phase === 'previewing')
      heading?.focus();
    else
      serviceForm?.focusFirstError();
  }
  async function save() {
    const d = await add.save();
    if (disposed || !d)
      return;
    toasts.push({ kind: 'success', message: `Added ${savedService} to compose.yaml. Run Up to start it.` });
    if (get(checksPartial))
      toasts.push({ kind: 'warning', message: 'Port and IP checks were partial (Docker unreachable)' });
    onadded(d, get(checksPartial));
  }
  async function reload(keep: boolean) {
    if (reloading)
      return;
    reloading = true;
    const mine = ++generation;
    try {
      const d = await onreload();
      if (disposed || mine !== generation)
        return;
      if (keep) {
        add.reload(d);
        await preview();
      }
      else {
        add.discard(d);
        form = get(add.form);
        name = get(add.name);
      }
    }
    catch (e) {
      if (!disposed && mine === generation) {
        if (e instanceof ApiError && e.status === 503)
          void refreshStatus();
        toasts.push({ kind: 'danger', message: `Could not reload the stack: ${e instanceof Error ? e.message : String(e)}` });
      }
    }
    finally {
      if (!disposed && mine === generation)
        reloading = false;
    }
  }
</script>
{#snippet networkPicker(f: Form, rowErrors: Record<number, string>)}
  <NetworkPicker idPrefix="svc-networks" value={f.networks} onchange={(v) => (form.networks = v)} errors={rowErrors} />
{/snippet}
<div class="panel">
  {#if $notice}<Banner kind="danger" title="Could not complete the request"><span class="message">{$notice.message}</span></Banner>{/if}
  {#if $phase === 'conflict'}
    <Banner kind="danger" title="Save refused"><span class="message">{$conflictMessage}</span></Banner>
    <div class="row"><Button variant="primary" loading={reloading} onclick={() => reload(true)}>Reload</Button><Button variant="secondary" disabled={reloading} onclick={() => reload(false)}>Discard my changes</Button></div>
  {/if}
  {#if $phase === 'previewing' || $phase === 'saving'}
    <h3 tabindex="-1" bind:this={heading}>Review the service to add</h3>
    {#if $previewText}<DiffView original={$previewText.original} modified={$previewText.modified} ariaLabel="Changes to compose.yaml" />{/if}
  {/if}
  <div class="form-card" hidden={$phase === 'previewing' || $phase === 'saving'} inert={$phase === 'busy' || $phase === 'saving' || reloading}>
    <ServiceForm bind:this={serviceForm} bind:form bind:name showName errors={$errors} {networkPicker} onbrowse={(mode, cb) => (browse = { mode, cb })} />
  </div>
  <div class="row">
    {#if $phase === 'previewing' || $phase === 'saving'}
      <Button variant="secondary" disabled={$phase === 'saving'} onclick={() => add.cancelPreview()}>Back to editing</Button>
      <Button variant="primary" loading={$phase === 'saving'} onclick={save}>Save</Button>
    {:else}
      <Button variant="primary" loading={$phase === 'busy'} disabledReason={$phase === 'conflict' ? 'Resolve the conflict first (Reload or Discard)' : undefined} onclick={preview}>Preview changes</Button>
    {/if}
    <Button variant="secondary" onclick={oncancel}>Cancel</Button>
  </div>
</div>
<FileBrowser open={browse !== undefined} mode={browse?.mode ?? 'dir'} title={browse?.mode === 'file' ? 'Choose a Dockerfile' : 'Choose a build context'} onpick={(path) => { browse?.cb(path); browse = undefined; }} onclose={() => (browse = undefined)} />
<style>
  .panel { display: flex; flex-direction: column; gap: var(--space-3); min-width: 0; }
  .row { display: flex; flex-wrap: wrap; gap: var(--space-2); }
  .form-card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); }
  .message { white-space: pre-wrap; overflow-wrap: anywhere; }
  [hidden] { display: none; }
</style>
