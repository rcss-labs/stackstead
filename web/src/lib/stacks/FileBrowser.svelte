<script lang="ts">
  import { api } from '../api/client';
  import { ApiError } from '../api/client';
  import type { FSEntry } from '../api/types';
  import Dialog from '../components/Dialog.svelte';
  import Spinner from '../components/Spinner.svelte';

  let {
    open,
    mode,
    title,
    onpick,
    onclose,
    filter,
  }: {
    open: boolean;
    mode: 'dir' | 'file';
    title: string;
    onpick: (path: string) => void;
    onclose: () => void;
    /** Files for which this returns true are listed first; others are still shown. */
    filter?: (e: FSEntry) => boolean;
  } = $props();

  const isDockerfile = (e: FSEntry) => /^Dockerfile/.test(e.name) || /\.Dockerfile$/.test(e.name);
  const preferred = $derived(filter ?? isDockerfile);

  // path '' is the list of allowed roots; root is the allowed root containing path.
  let path = $state('');
  let root = $state('');
  let entries = $state<FSEntry[]>([]);
  let loading = $state(false);
  let error = $state('');
  let seq = 0;

  const join = (dir: string, name: string) => (dir.endsWith('/') ? dir + name : `${dir}/${name}`);

  const crumbs = $derived.by(() => {
    if (!path) return [];
    const out = [{ label: root, path: root }];
    let acc = root;
    for (const part of path.slice(root.length).split('/').filter(Boolean)) {
      acc = join(acc, part);
      out.push({ label: part, path: acc });
    }
    return out;
  });

  const sorted = $derived.by(() => {
    const dirs = entries.filter((e) => e.isDir);
    const files = entries.filter((e) => !e.isDir);
    if (mode === 'dir') return [...dirs, ...files];
    return [...dirs, ...files.filter((e) => preferred(e)), ...files.filter((e) => !preferred(e))];
  });

  function describe(err: unknown): string {
    if (err instanceof ApiError) {
      if (err.status === 403) {
        return /permission/i.test(err.message)
          ? 'Permission denied: this directory cannot be read.'
          : 'This path is outside the allowed directories.';
      }
      if (err.status === 404) return 'No such directory.';
      return err.message;
    }
    return 'Could not list this directory.';
  }

  async function load(next: string, nextRoot: string) {
    const mine = ++seq;
    loading = true;
    error = '';
    try {
      const list = await api.fs(next || undefined);
      if (mine !== seq) return;
      entries = list;
      path = next;
      root = nextRoot;
    } catch (err) {
      if (mine !== seq) return;
      error = describe(err);
      entries = [];
      path = next;
      root = nextRoot;
    } finally {
      if (mine === seq) loading = false;
    }
  }

  $effect(() => {
    if (open) {
      void load('', '');
    } else {
      seq++;
    }
  });

  function choose(e: FSEntry) {
    if (e.isDir) {
      const target = path ? join(path, e.name) : e.name;
      void load(target, path ? root : e.name);
    } else if (mode === 'file') {
      onpick(join(path, e.name));
    }
  }
</script>

<Dialog {open} {title} {onclose}>
  <nav aria-label="Current location" class="crumbs">
    {#if path}
      <button type="button" onclick={() => load('', '')}>Allowed directories</button>
      {#each crumbs as crumb, i (crumb.path)}
        <span aria-hidden="true">/</span>
        {#if i === crumbs.length - 1}
          <span aria-current="location" class="here">{crumb.label}</span>
        {:else}
          <button type="button" onclick={() => load(crumb.path, root)}>{crumb.label}</button>
        {/if}
      {/each}
    {:else}
      <span aria-current="location" class="here">Allowed directories</span>
    {/if}
  </nav>

  {#if loading}<Spinner />{/if}
  {#if error}<p role="alert" class="error"><span aria-hidden="true">✖ </span>{error}</p>{/if}

  {#if !loading && !error}
    {#if sorted.length === 0}
      <p class="muted">This directory is empty.</p>
    {:else}
      <ul class="entries">
        {#each sorted as e (e.name)}
          <li>
            <button type="button" disabled={!e.isDir && mode === 'dir'} onclick={() => choose(e)}>
              <span aria-hidden="true">{e.isDir ? '📁' : '📄'} </span>{e.name}
            </button>
          </li>
        {/each}
      </ul>
    {/if}
  {/if}

  <div class="actions">
    <button type="button" onclick={onclose}>Cancel</button>
    {#if mode === 'dir' && path}
      <button type="button" class="primary" disabled={!!error || loading} onclick={() => onpick(path)}>Use this directory</button>
    {/if}
  </div>
</Dialog>

<style>
  .crumbs { display: flex; flex-wrap: wrap; gap: var(--space-1); align-items: center; overflow-wrap: anywhere; }
  .here { font-weight: 600; }
  .entries { list-style: none; padding: 0; display: flex; flex-direction: column; gap: var(--space-1); }
  .entries button { width: 100%; text-align: left; }
  .error { color: var(--danger); overflow-wrap: anywhere; }
  .muted { color: var(--muted); }
  .actions { display: flex; gap: var(--space-2); justify-content: flex-end; }
  button {
    padding: var(--space-1) var(--space-3);
    background: var(--surface-raised);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
    cursor: pointer;
    color: inherit;
  }
  button:disabled { opacity: 0.6; cursor: default; }
  .crumbs button { border: none; background: none; text-decoration: underline; padding: 0; }
</style>
