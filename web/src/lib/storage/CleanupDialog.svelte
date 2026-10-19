<script lang="ts">
  import { onMount, onDestroy, tick } from 'svelte';
  import { api, ApiError } from '$lib/api/client';
  import type { StoragePlanRequest, StoragePlan, Operation } from '$lib/api/types';
  import Dialog from '$lib/components/Dialog.svelte';
  import Button from '$lib/components/Button.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import OperationPanel from '$lib/stacks/OperationPanel.svelte';
  import { refreshRunningOps } from '$lib/stores/stacks';
  import { toasts } from '$lib/stores/toasts';
  import { link } from '$lib/router';
  import { reclaimText, sizeText } from './storage';

  let { request, onclose, onfinished }: { request: StoragePlanRequest; onclose: () => void; onfinished?: (op: Operation) => void } = $props();
  const uid = $props.id();
  let plan = $state<StoragePlan>();
  let checking = $state(true);
  let error = $state('');
  let fieldError = $state('');
  let phrase = $state('');
  let now = $state(Date.now());
  let expired = $state(false);
  let confirming = $state(false);
  let operation = $state<Operation>();
  let finished = $state(false);
  let field = $state<HTMLInputElement>();
  let generation = 0;
  let mounted = false;
  const seconds = $derived(plan ? Math.max(0, Math.ceil((Date.parse(plan.expiresAt) - now) / 1000)) : 0);
  const stale = $derived(expired || (!!plan && seconds === 0));
  const volumePlan = $derived(plan?.kind === 'volume' || plan?.kind === 'volumes');
  const canClose = $derived(!confirming && (!operation || finished));
  function close() { if (canClose) onclose(); }
  async function check() {
    const mine = ++generation;
    checking = true; error = ''; fieldError = ''; phrase = ''; plan = undefined; expired = false;
    try {
      const fresh = await api.createStoragePlan(request);
      if (!mounted || mine !== generation) return;
      plan = fresh; now = Date.now();
    } catch (e) {
      if (mounted && mine === generation) error = e instanceof Error ? e.message : String(e);
    } finally {
      if (mounted && mine === generation) {
        checking = false;
        await tick();
        if (mounted && mine === generation) field?.focus();
      }
    }
  }
  onMount(() => { mounted = true; void check(); });
  onDestroy(() => { mounted = false; generation++; });
  $effect(() => {
    if (!plan || operation) return;
    const timer = setInterval(() => now = Date.now(), 1000);
    return () => clearInterval(timer);
  });
  async function confirm() {
    now = Date.now();
    if (!plan || stale || confirming || phrase !== plan.confirmPhrase) return;
    confirming = true; error = ''; fieldError = '';
    try {
      const op = await api.confirmStoragePlan(plan.id, phrase);
      if (mounted) operation = op;
    } catch (e) {
      if (!mounted) return;
      if (e instanceof ApiError && e.status === 404) expired = true;
      else if (e instanceof ApiError && e.fields.some(f => f.field === 'confirm')) {
        fieldError = e.fields.find(f => f.field === 'confirm')?.message ?? e.message;
        await tick(); field?.focus();
      } else error = e instanceof Error ? e.message : String(e);
    } finally { if (mounted) confirming = false; }
  }
  function ended(op: Operation) {
    if (finished) return;
    finished = true;
    toasts.push({ kind: op.state === 'succeeded' ? 'success' : op.state === 'cancelled' ? 'warning' : 'danger', message: `Storage cleanup ${op.state.replaceAll('_', ' ')}` });
    refreshRunningOps();
    onfinished?.(op);
  }
  async function cancel(id: string) {
    try { await api.cancel(id); }
    catch (e) { toasts.push({kind:'danger',message:`Could not cancel: ${e instanceof Error ? e.message : String(e)}`}); }
  }
</script>

<Dialog open title="Clean up storage" onclose={close}>
  {#if operation}
    <OperationPanel operationId={operation.id} oncancel={cancel} onfinished={ended} />
    {#if finished}<div class="actions"><Button variant="secondary" onclick={close}>Close</Button></div>{/if}
  {:else if checking}
    <p class="cluster"><Spinner /> Checking current state…</p>
    <div class="actions"><Button variant="secondary" onclick={close}>Close</Button></div>
  {:else}
    {#if error}<p role="alert" class="error">{error}</p>{/if}
    {#if plan}
      {#if !plan.items.length}<p>Nothing to clean</p>
      {:else}
        <h3>{plan.mode === 'all-unused' ? 'Expected items' : 'Items to delete'}</h3>
        <ul>{#each plan.items as item}<li><span class="mono">{item.ref}</span><span class="mono muted">{sizeText(item.bytes)}</span></li>{/each}</ul>
        <p>{reclaimText(plan.reclaim)}</p>
      {/if}
      {#if plan.protected.length}
        <h3>Protected</h3>
        <ul>{#each plan.protected as item}<li class="protected"><span class="mono">{item.ref}</span><span>{item.reason}{#if item.stack} · <a href="/stacks/{encodeURIComponent(item.stack)}" use:link>{item.stack}</a> ({item.stackState}){/if}</span></li>{/each}</ul>
      {/if}
      {#each plan.warnings as warning}<p class="warning">{warning}</p>{/each}
      {#if plan.items.length}
        {#if volumePlan}<p class="warning">Volumes hold data. This cannot be undone.</p>{/if}
        <p class="muted">Expires in {Math.floor(seconds / 60)}:{String(seconds % 60).padStart(2, '0')}</p>
        {#if stale}<p>This plan has expired. Re-check current state before deleting.</p>
        {:else}
          <p id="{uid}-phrase"><code>{plan.confirmPhrase}</code>{#if plan.mode === 'all-unused'} <span class="warning">not exact</span>{/if}</p>
          <label for="{uid}-confirm">Type {plan.confirmPhrase} to confirm</label>
          <input id="{uid}-confirm" class="mono" bind:this={field} bind:value={phrase} disabled={confirming} aria-describedby="{uid}-phrase{fieldError ? ` ${uid}-error` : ''}" aria-invalid={fieldError ? 'true' : undefined} autocomplete="off" spellcheck="false" />
          {#if fieldError}<p id="{uid}-error" class="error" role="alert">{fieldError}</p>{/if}
        {/if}
      {/if}
    {/if}
    <div class="actions">
      <Button variant="secondary" disabled={confirming} onclick={close}>Close</Button>
      {#if plan?.items.length}
        {#if stale}<Button variant="secondary" onclick={check}>Re-check</Button>
        {:else}<Button variant="danger" confirm disabled={phrase !== plan.confirmPhrase || confirming} loading={confirming} onclick={confirm}>Delete</Button>{/if}
      {/if}
    </div>
  {/if}
</Dialog>

<style>
  h3 { font-size: 13px; margin: 0; }
  p { margin: 0; overflow-wrap: anywhere; }
  ul { list-style: none; margin: 0; padding: 0; display: grid; gap: var(--space-2); }
  li { display: flex; justify-content: space-between; gap: var(--space-3); flex-wrap: wrap; overflow-wrap: anywhere; }
  .protected { color: var(--muted); }
  .mono, code { font-family: var(--mono); overflow-wrap: anywhere; }
  .muted { color: var(--muted); }
  .warning { color: var(--warning); }
  .error { color: var(--danger); }
  label { font-size: 13px; overflow-wrap: anywhere; }
  input { width: 100%; box-sizing: border-box; min-width: 0; }
  .actions { display: flex; justify-content: flex-end; gap: var(--space-2); }
</style>
