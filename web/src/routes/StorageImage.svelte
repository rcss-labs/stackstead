<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, ApiError } from '$lib/api/client';
  import type { StorageImageDetail, StoragePlanRequest } from '$lib/api/types';
  import Button from '$lib/components/Button.svelte';
  import CopyButton from '$lib/components/CopyButton.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import { link, navigate } from '$lib/router';
  import { changes, live } from '$lib/stores/live';
  import { operationsRefresh } from '$lib/stores/stacks';
  import { createStorageRefresh } from '$lib/storage/controller';
  import { deleteDisabledReason, sizeText } from '$lib/storage/storage';
  import CleanupDialog from '$lib/storage/CleanupDialog.svelte';
  let { id }: { id: string } = $props();
  async function load(): Promise<StorageImageDetail | null> {
    try { return await api.storageImage(id); }
    catch (e) { if (e instanceof ApiError && e.status === 404) return null; throw e; }
  }
  const controller = createStorageRefresh({load, changes, operations: operationsRefresh, refreshing: () => false});
  const {data, error} = controller;
  onMount(() => controller.start());
  onDestroy(() => controller.stop());
  const item = $derived($data);
  const reason = $derived(item ? deleteDisabledReason(item, $live.docker === 'unavailable' || $live.events === 'disconnected') : undefined);
  let cleanup = $state<StoragePlanRequest>();
</script>

<section class="page">
  <a class="muted" href="/storage?tab=images" use:link>Back to images</a>
  <header>
      <h1 class="mono">{item ? item.tags[0] ?? item.id.replace(/^sha256:/, '').slice(0, 12) : $error ? 'Image unavailable' : item === null ? 'Image not found' : 'Image details'}</h1>
      {#if item}
      <span class="tag">{item.containers.length ? 'in use' : item.dangling ? 'dangling' : 'no containers'}</span>
      <CopyButton text={item.id} label="Copy image ID" />
      <Button variant="danger" disabledReason={reason} onclick={() => cleanup = {kind:'image', ref: item.id}}>Delete</Button>
      {/if}
  </header>
  {#if $error}<p role="alert">{$error}</p>
  {:else if item}
    <dl><dt>Tags</dt><dd>{item.tags.join(', ') || '—'}</dd>
        <dt>Digests</dt><dd>{item.digests.join(', ') || '—'}</dd>
        <dt>Created</dt><dd><RelativeTime iso={item.created} /></dd>
        <dt>Size</dt><dd>{sizeText(item.size)}</dd>
        <dt>Architecture / OS</dt><dd>{item.architecture} / {item.os}</dd>
        <dt>Layers</dt><dd>{item.layers}</dd></dl>
    <h2>Containers</h2>
    <ul>{#each item.containers as consumer}<li><a href="/containers/{encodeURIComponent(consumer.name)}" use:link>{consumer.name}</a> <span class="muted">{consumer.state}</span></li>{:else}<li class="muted">No containers</li>{/each}</ul>
    <h2>Declared by</h2>
    <ul>{#each item.declaredBy as declaration}<li><a href="/stacks/{encodeURIComponent(declaration.stack)}" use:link>{declaration.stack}</a> · {declaration.service} <span class="muted">({declaration.stackState})</span></li>{:else}<li class="muted">No declarations</li>{/each}</ul>
    <details><summary>Labels</summary><dl>{#each Object.entries(item.labels) as [key, value]}<dt>{key}</dt><dd>{value}</dd>{:else}<dd class="muted">No labels</dd>{/each}</dl></details>
  {:else if item === undefined}<p class="cluster"><Spinner /> Loading image…</p>{/if}
</section>

{#if cleanup}<CleanupDialog request={cleanup} onclose={() => cleanup = undefined} onfinished={() => navigate('/storage?tab=images')} />{/if}

<style>
  .page { min-width: 0; }
  header { display: flex; align-items: center; flex-wrap: wrap; gap: var(--space-3); margin: var(--space-4) 0; }
  h1 { margin: 0; font-size: 22px; overflow-wrap: anywhere; min-width: 0; }
  h2 { font-size: 14px; margin: var(--space-5) 0 var(--space-2); }
  .mono, dd { font-family: var(--mono); }
  .tag { border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 1px 5px; font-size: 11px; }
  .muted, dt { color: var(--muted); }
  a { color: var(--text); overflow-wrap: anywhere; }
  a:hover { color: var(--accent); }
  dl { display: grid; grid-template-columns: 140px minmax(0, 1fr); gap: var(--space-3); padding: var(--space-4); border: 1px solid var(--border); border-radius: var(--radius-md); background: var(--surface); font-size: 13px; }
  dt, dd { margin: 0; overflow-wrap: anywhere; }
  ul { list-style: none; padding: 0; display: grid; gap: var(--space-2); }
  li, p { overflow-wrap: anywhere; }
  details { margin-top: var(--space-5); }
  summary { cursor: pointer; }
  @media (max-width: 480px) { dl { grid-template-columns: minmax(0, 1fr); } dd { margin-bottom: var(--space-2); } }
</style>
