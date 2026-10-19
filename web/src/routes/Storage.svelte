<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, ApiError } from '$lib/api/client';
  import type { StorageConsumer, StorageDeclaration, StorageKindTotals } from '$lib/api/types';
  import Banner from '$lib/components/Banner.svelte';
  import Button from '$lib/components/Button.svelte';
  import RelativeTime from '$lib/components/RelativeTime.svelte';
  import Spinner from '$lib/components/Spinner.svelte';
  import Tabs from '$lib/components/Tabs.svelte';
  import HostTile from '$lib/dashboard/HostTile.svelte';
  import { formatBytes } from '$lib/format';
  import { link, navigate, route } from '$lib/router';
  import { changes, live } from '$lib/stores/live';
  import { operationsRefresh } from '$lib/stores/stacks';
  import { createStorageRefresh } from '$lib/storage/controller';
  import { deleteDisabledReason, filterImages, filterVolumes, imageChips, imageCounts, imagePath, imageReclaimRange, sizeText, volumeChips, volumeCounts, type ImageChip, type VolumeChip } from '$lib/storage/storage';

  const tabs = [{ id: 'overview', label: 'Overview' }, { id: 'images', label: 'Images' }, { id: 'volumes', label: 'Volumes' }, { id: 'build-cache', label: 'Build cache' }];
  const tab = $derived(tabs.find(t => t.id === $route.query.get('tab'))?.id ?? 'overview');
  const message = (e: unknown) => e instanceof Error ? e.message : String(e);
  async function load() {
    // Independent reads let the persistent action log remain usable when Docker is down.
    const [overview, images, volumes, cache, actions] = await Promise.allSettled([
      api.storageOverview(), api.storageImages(), api.storageVolumes(), api.storageBuildCache(), api.recentActions({ kind: 'storage.', limit: 20 }),
    ]);
    return {
      overview: overview.status === 'fulfilled' ? overview.value : undefined,
      images: images.status === 'fulfilled' ? images.value : undefined,
      volumes: volumes.status === 'fulfilled' ? volumes.value : undefined,
      cache: cache.status === 'fulfilled' ? cache.value : undefined,
      actions: actions.status === 'fulfilled' ? actions.value : [],
      errors: {
        overview: overview.status === 'rejected' ? message(overview.reason) : '',
        images: images.status === 'rejected' ? message(images.reason) : '',
        volumes: volumes.status === 'rejected' ? message(volumes.reason) : '',
        'build-cache': cache.status === 'rejected' ? message(cache.reason) : '',
        actions: actions.status === 'rejected' ? message(actions.reason) : '',
      },
    };
  }
  const controller = createStorageRefresh({ load, changes, operations: operationsRefresh, refreshing: d => [d.overview?.images, d.overview?.containers, d.overview?.volumes, d.overview?.buildCache, d.images?.totals, d.volumes?.totals, d.cache?.totals].some(t => t?.refreshing) });
  const { data } = controller;
  onMount(() => controller.start());
  onDestroy(() => controller.stop());
  let imageChip = $state<ImageChip>('all');
  let volumeChip = $state<VolumeChip>('all');
  let search = $state('');
  const images = $derived($data?.images?.images ?? []);
  const volumes = $derived($data?.volumes?.volumes ?? []);
  const imageCount = $derived(imageCounts(images));
  const danglingCandidates = $derived(images.filter(image => image.dangling && !image.containers.length));
  const danglingReclaim = $derived(imageReclaimRange(danglingCandidates));
  const volumeCount = $derived(volumeCounts(volumes));
  const shownImages = $derived(filterImages(images, imageChip, search));
  const shownVolumes = $derived(filterVolumes(volumes, volumeChip, search));
  const dockerDown = $derived($live.docker === 'unavailable' || $live.events === 'disconnected');
  const cleanupUnavailable = 'Cleanup confirmation is not available yet';
  const kindError = $derived($data?.errors[tab as 'overview' | 'images' | 'volumes' | 'build-cache']);
  let recalculating = $state(false);
  let cooldownUntil = $state(0);
  let secondsLeft = $state(0);
  let refreshError = $state('');
  let mounted = false;
  onMount(() => { mounted = true; return () => { mounted = false; }; });
  let cooldownTimer: ReturnType<typeof setTimeout> | undefined;
  function updateCooldown() {
    clearTimeout(cooldownTimer);
    secondsLeft = Math.max(0, Math.ceil((cooldownUntil - Date.now()) / 1000));
    if (mounted && secondsLeft && document.visibilityState !== 'hidden') cooldownTimer = setTimeout(updateCooldown, 1000);
  }
  function visibility() { updateCooldown(); }
  onMount(() => { document.addEventListener('visibilitychange', visibility); return () => document.removeEventListener('visibilitychange', visibility); });
  onDestroy(() => clearTimeout(cooldownTimer));
  const recalculateReason = $derived(dockerDown ? 'Docker is unavailable' : recalculating ? 'Requesting a size reading' : secondsLeft ? `Recalculate available in ${secondsLeft}s` : undefined);
  async function recalculate() {
    if (recalculateReason) return;
    recalculating = true; refreshError = '';
    try {
      await api.refreshStorage();
      cooldownUntil = Date.now() + 60_000;
      controller.refresh();
    } catch (e) {
      refreshError = message(e);
      if (e instanceof ApiError && e.status === 429) {
        const retry = (e.body as { retryAfter?: unknown } | undefined)?.retryAfter;
        cooldownUntil = Date.now() + (typeof retry === 'number' && retry > 0 ? retry : 60) * 1000;
      }
    } finally { recalculating = false; updateCooldown(); }
  }
  function selectTab(id: string) { if (id !== tab) { search = ''; navigate(`/storage?tab=${id}`); } }
  const shortId = (id: string) => id.replace(/^sha256:/, '').slice(0, 12);
  const stackPath = (name: string) => `/stacks/${encodeURIComponent(name)}`;
  const volumePath = (name: string) => `/storage/volumes/${encodeURIComponent(name)}`;
  const knownTime = (iso: string | null) => iso && !iso.startsWith('0001-');
  const overviewKinds = $derived($data?.overview ? [
    { label: 'Images', total: $data.overview.images, target: 'images' },
    { label: 'Container layers', total: $data.overview.containers, target: undefined },
    { label: 'Volumes', total: $data.overview.volumes, target: 'volumes' },
    { label: 'Build cache', total: $data.overview.buildCache, target: 'build-cache' },
  ] : []);
</script>

{#snippet sizes(total: StorageKindTotals)}
  <p class="age">{#if total.sizesAt}Sizes from <RelativeTime iso={total.sizesAt} />{:else}Sizes {total.refreshing ? 'calculating…' : 'not available yet'}{/if}{#if total.refreshing && total.sizesAt} · refreshing…{/if}</p>
  {#if total.error}<Banner kind="warning" title="Sizes unavailable">{total.error}</Banner>{/if}
{/snippet}
{#snippet consumers(rows: StorageConsumer[])}
  {#each rows as c}<div class:muted={c.state !== 'running'}><a class="mono" href={`/containers/${encodeURIComponent(c.name)}`} use:link>{c.name}</a> <span class="muted">({c.state})</span></div>{:else}—{/each}
{/snippet}
{#snippet declarations(rows: StorageDeclaration[])}
  {#each rows as d}<div><a href={stackPath(d.stack)} use:link>{d.stack}</a> <span class="muted">({d.stackState})</span></div>{:else}—{/each}
{/snippet}

<section class="page" aria-labelledby="storage-heading">
  <header class="head"><h1 id="storage-heading">Storage</h1><Button variant="secondary" disabledReason={recalculateReason} onclick={() => void recalculate()}>Recalculate</Button></header>
  {#if refreshError}<Banner kind="warning" title="Could not recalculate">{refreshError}</Banner>{/if}
  <Tabs {tabs} selected={tab} onselect={selectTab} ariaLabel="Storage sections">
    {#snippet children(selected)}
      {#if !$data}<Spinner label="Loading storage" />
      {:else}
        {#if kindError}<Banner kind="danger" title="Could not load storage">{kindError}</Banner><p><Button variant="secondary" onclick={() => controller.refresh()}>Retry</Button></p>{/if}
        {#if selected === 'overview'}
          {#if $data.overview}
            <div class="tiles">{#each overviewKinds as kind}<HostTile tile={{ id: kind.label, label: kind.label, value: sizeText(kind.total.size, kind.total.refreshing), sub: `${kind.total.count} · ${sizeText(kind.total.reclaimable)} reclaimable`, href: kind.target ? `/storage?tab=${kind.target}` : undefined }} />{/each}</div>
            <div class="ages">{#each overviewKinds as kind}<div><strong>{kind.label}</strong>{@render sizes(kind.total)}</div>{/each}</div>
            {#if $data.overview.declarationsUnknown.length}<Banner kind="warning" title={`Declarations unknown for: ${$data.overview.declarationsUnknown.join(', ')}`}>Declared volumes remain protected where ownership is known. Re-check the stack configuration.</Banner>{/if}
            <section class="card pad"><h2>Largest container writable layers</h2>
              <ul class="layers">{#each $data.overview.topContainers.slice(0, 5) as c}<li><a class="mono" href={`/containers/${encodeURIComponent(c.name)}`} use:link>{c.name}</a>{#if c.stack}<a href={stackPath(c.stack)} use:link>{c.stack}</a>{/if}<span class="mono">{sizeText(c.sizeRw)}</span></li>{:else}<li>No container layers.</li>{/each}</ul>
            </section>
          {/if}
          <section class="actions"><h2>Recent storage actions</h2>
            {#if $data.errors.actions}<Banner kind="warning" title="Could not load recent actions">{$data.errors.actions}</Banner>
            {:else if !$data.actions.length}<p class="muted">No storage actions yet.</p>
            {:else}<div class="card"><!-- svelte-ignore a11y_no_redundant_roles -->
<table role="table" aria-label="Recent storage actions"><!-- svelte-ignore a11y_no_redundant_roles -->
<thead role="rowgroup"><!-- svelte-ignore a11y_no_redundant_roles -->
<tr role="row">{#each ['Time', 'User', 'Kind', 'Ref', 'Result', 'Bytes'] as label}<!-- svelte-ignore a11y_no_redundant_roles -->
<th role="columnheader" scope="col">{label}</th>{/each}</tr></thead><!-- svelte-ignore a11y_no_redundant_roles -->
<tbody role="rowgroup">
              {#each $data.actions.slice(0, 20) as action (action.id)}<!-- svelte-ignore a11y_no_redundant_roles -->
<tr role="row"><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Time"><RelativeTime iso={action.ts} /></td><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="User">{action.username}</td><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Kind" class="mono">{action.kind}</td><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Ref" class="mono" title={action.detail}>{action.target}</td><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Result">{action.result}</td><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Bytes" class="mono">{sizeText(action.bytes)}</td></tr>{/each}
            </tbody></table></div>{/if}
          </section>
        {:else if selected === 'images' && $data.images}
          <div class="toolbar"><div class="chips" role="group" aria-label="Filter images">{#each imageChips as chip}<button type="button" aria-pressed={imageChip === chip.id} onclick={() => imageChip = chip.id}>{chip.label} <span class="mono">{imageCount[chip.id]}</span></button>{/each}</div><label class="search">Search images<input type="search" aria-label="Search images" bind:value={search} /></label></div>
          <div class="summary">{@render sizes($data.images.totals)}<div class="image-cleanup"><Button variant="danger" disabledReason={dockerDown ? 'Docker is unavailable' : cleanupUnavailable}><span>Clean up dangling images ({danglingCandidates.length} · {danglingReclaim})</span></Button></div></div>
          {#if shownImages.length}<div class="card"><!-- svelte-ignore a11y_no_redundant_roles -->
<table role="table" aria-label="Images"><!-- svelte-ignore a11y_no_redundant_roles -->
<thead role="rowgroup"><!-- svelte-ignore a11y_no_redundant_roles -->
<tr role="row">{#each ['Image', 'Size', 'Created', 'Containers', 'Declared by', 'Status', 'Actions'] as label}<!-- svelte-ignore a11y_no_redundant_roles -->
<th role="columnheader" scope="col">{label}</th>{/each}</tr></thead><!-- svelte-ignore a11y_no_redundant_roles -->
<tbody role="rowgroup">
            {#each shownImages as image (image.id)}<!-- svelte-ignore a11y_no_redundant_roles -->
<tr role="row">
              <!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Image"><div class="refs">{#each image.tags as tag}<a class="tag mono" href={imagePath(image.id)} use:link>{tag}</a>{:else}<a class="mono" href={imagePath(image.id)} use:link>&lt;none&gt; {shortId(image.id)}</a>{/each}</div></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Size" class="mono" title={image.size !== null && image.sharedSize !== null ? `unique ≈ ${formatBytes(Math.max(0, image.size - image.sharedSize))}` : undefined}>{sizeText(image.size, $data.images.totals.refreshing)}</td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Created">{#if knownTime(image.created)}<RelativeTime iso={image.created} />{:else}—{/if}</td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Containers"><div><span class="muted">{image.containers.length} containers</span>{@render consumers(image.containers)}</div></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Declared by"><div>{@render declarations(image.declaredBy)}</div></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Status"><span class="tag">{image.containers.length ? 'in use' : image.dangling ? 'dangling' : 'no containers'}</span></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Actions"><Button variant="danger" size="sm" disabledReason={deleteDisabledReason(image, dockerDown) ?? cleanupUnavailable}>Delete</Button></td>
            </tr>{/each}
          </tbody></table></div>{:else}<p class="muted">No images match the current filter.</p>{/if}
        {:else if selected === 'volumes' && $data.volumes}
          <div class="toolbar"><div class="chips" role="group" aria-label="Filter volumes">{#each volumeChips as chip}<button type="button" aria-pressed={volumeChip === chip.id} onclick={() => volumeChip = chip.id}>{chip.label} <span class="mono">{volumeCount[chip.id]}</span></button>{/each}</div><label class="search">Search volumes<input type="search" aria-label="Search volumes" bind:value={search} /></label></div>
          <div class="summary">{@render sizes($data.volumes.totals)}<Button variant="danger" disabledReason={dockerDown ? 'Docker is unavailable' : cleanupUnavailable}>Clean up unused volumes</Button></div>
          {#if shownVolumes.length}<div class="card"><!-- svelte-ignore a11y_no_redundant_roles -->
<table role="table" aria-label="Volumes"><!-- svelte-ignore a11y_no_redundant_roles -->
<thead role="rowgroup"><!-- svelte-ignore a11y_no_redundant_roles -->
<tr role="row">{#each ['Name', 'Driver', 'Size', 'Containers', 'Declared by', 'Status', 'Actions'] as label}<!-- svelte-ignore a11y_no_redundant_roles -->
<th role="columnheader" scope="col">{label}</th>{/each}</tr></thead><!-- svelte-ignore a11y_no_redundant_roles -->
<tbody role="rowgroup">
            {#each shownVolumes as volume (volume.name)}<!-- svelte-ignore a11y_no_redundant_roles -->
<tr role="row">
              <!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Name"><a class="mono" href={volumePath(volume.name)} use:link>{volume.name}</a></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Driver">{volume.driver}</td><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Size" class="mono">{sizeText(volume.size, volume.sizesPending)}</td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Containers"><div>{@render consumers(volume.containers)}</div></td><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Declared by"><div>{@render declarations(volume.declaredBy)}</div></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Status"><div><span class="tag">{volume.containers.length ? 'in use' : volume.protection ? 'unused · protected' : 'unused'}</span>
                {#if volume.protection}{@const declaration = volume.declaredBy.find(d => d.stack === volume.protection?.stack)}<p class="protection">{declaration ? 'declared by stack' : 'protected by stack'} <a href={stackPath(volume.protection.stack)} use:link>{volume.protection.stack}</a>{#if declaration} ({declaration.stackState}){/if} — remove via the stack</p>{/if}
              </div></td>
              <!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Actions">{#if !volume.protection && !volume.containers.length}<Button variant="danger" size="sm" disabledReason={deleteDisabledReason(volume, dockerDown) ?? cleanupUnavailable}>Delete</Button>{/if}</td>
            </tr>{/each}
          </tbody></table></div>{:else}<p class="muted">No volumes match the current filter.</p>{/if}
        {:else if selected === 'build-cache' && $data.cache}
          <div class="summary"><div><h2>{sizeText($data.cache.totals.size, $data.cache.totals.refreshing)} · {$data.cache.totals.count} records</h2><p class="muted">{sizeText($data.cache.totals.reclaimable)} reclaimable</p>{@render sizes($data.cache.totals)}</div><Button variant="danger" disabledReason={dockerDown ? 'Docker is unavailable' : cleanupUnavailable}>Clean up build cache</Button></div>
          {#if $data.cache.records.length}<div class="card"><!-- svelte-ignore a11y_no_redundant_roles -->
<table role="table" aria-label="Build cache"><!-- svelte-ignore a11y_no_redundant_roles -->
<thead role="rowgroup"><!-- svelte-ignore a11y_no_redundant_roles -->
<tr role="row">{#each ['Type', 'Description', 'Size', 'In use', 'Shared', 'Last used'] as label}<!-- svelte-ignore a11y_no_redundant_roles -->
<th role="columnheader" scope="col">{label}</th>{/each}</tr></thead><!-- svelte-ignore a11y_no_redundant_roles -->
<tbody role="rowgroup">
            {#each $data.cache.records as record (record.id)}<!-- svelte-ignore a11y_no_redundant_roles -->
<tr role="row"><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Type">{record.type}</td><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Description"><div>{record.description}<div class="mono muted">{record.id}</div></div></td><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Size" class="mono">{sizeText(record.size, $data.cache.totals.refreshing)}</td><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="In use">{record.inUse ? 'Yes' : 'No'}</td><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Shared">{record.shared ? 'Yes' : 'No'}</td><!-- svelte-ignore a11y_no_redundant_roles -->
<td role="cell" data-label="Last used">{#if knownTime(record.lastUsedAt)}<RelativeTime iso={record.lastUsedAt!} />{:else}—{/if}</td></tr>{/each}
          </tbody></table></div>{:else}<p class="muted">No build cache records.</p>{/if}
        {/if}
      {/if}
    {/snippet}
  </Tabs>
</section>

<style>
  .page { min-width: 0; }
  .head, .toolbar, .summary { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--space-3); margin-bottom: var(--space-4); }
  .head h1 { margin: 0; }
  h2 { font-size: 14px; font-weight: 600; margin: 0 0 var(--space-3); }
  .tiles { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: var(--space-3); }
  .ages { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: var(--space-3); margin: var(--space-3) 0 var(--space-4); font-size: 12px; color: var(--muted); }
  .age { color: var(--muted); font-size: 12px; margin: var(--space-1) 0; }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); min-width: 0; }
  .pad { padding: var(--space-4); margin-top: var(--space-4); }
  .actions { margin-top: var(--space-5); }
  .layers { margin: 0; padding: 0; list-style: none; }
  .layers li { display: flex; flex-wrap: wrap; gap: var(--space-3); padding: var(--space-2) 0; overflow-wrap: anywhere; }
  .layers li > :last-child { margin-left: auto; }
  .image-cleanup { min-width: 0; max-width: 100%; }
  .image-cleanup :global(button) { height: auto; min-height: 30px; max-width: 100%; }
  .image-cleanup span { white-space: normal; overflow-wrap: anywhere; }
  .chips { display: flex; flex-wrap: wrap; gap: var(--space-1); }
  .chips button { border: 1px solid var(--border); background: var(--surface); color: var(--muted); border-radius: var(--radius-pill); padding: 4px 10px; font: inherit; font-size: 12px; cursor: pointer; }
  .chips button[aria-pressed='true'] { color: var(--accent); background: var(--accent-soft); border-color: var(--accent); }
  .chips span { margin-left: 4px; }
  .search { display: flex; flex-wrap: wrap; gap: var(--space-2); align-items: center; font-size: 12px; color: var(--muted); }
  .search input { width: 180px; max-width: 100%; box-sizing: border-box; }
  table { border-collapse: collapse; width: 100%; table-layout: fixed; }
  th { text-align: left; font-size: 12px; font-weight: 500; color: var(--muted); padding: var(--space-2) 12px; border-bottom: 1px solid var(--border); }
  td { font-size: 12.5px; padding: var(--pad) 12px; border-bottom: 1px solid var(--border); height: var(--row); vertical-align: middle; overflow-wrap: anywhere; }
  tbody tr:hover { background: var(--hover); }
  tbody tr:last-child td { border-bottom: 0; }
  .mono { font-family: var(--mono); font-size: 12px; }
  .muted { color: var(--muted); }
  a { color: var(--text); text-decoration: none; overflow-wrap: anywhere; }
  a:hover { color: var(--accent); }
  .refs { display: flex; flex-wrap: wrap; gap: 4px; }
  .tag { border: 1px solid var(--border); border-radius: var(--radius-sm); padding: 1px 5px; font-size: 11px; overflow-wrap: anywhere; }
  .protection { margin: var(--space-1) 0 0; font-size: 12px; color: var(--muted); }
  @media (max-width: 1100px) { .tiles, .ages { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
  @media (max-width: 720px) {
    table, tbody, tr, td { display: block; }
    thead { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); }
    tr { border-bottom: 1px solid var(--border); padding: var(--space-2); }
    tr:last-child { border-bottom: 0; }
    td { display: grid; grid-template-columns: 80px minmax(0, 1fr); align-items: start; gap: var(--space-3); height: auto; border: 0; padding: var(--space-1) var(--space-2); }
    td::before { content: attr(data-label); color: var(--muted); font: 12px var(--sans); }
    td > * { min-width: 0; }
    td[data-label='Actions'] { display: flex; justify-content: flex-end; }
    td[data-label='Actions']::before { content: none; }
  }
  @media (max-width: 400px) { .tiles, .ages { grid-template-columns: minmax(0, 1fr); } }
</style>
