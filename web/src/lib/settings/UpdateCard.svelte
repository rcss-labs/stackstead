<script lang="ts">
  import { onMount } from 'svelte';
  import { api, ApiError } from '../api/client';
  import type { UpdateStatus } from '../api/types';
  import Banner from '../components/Banner.svelte';
  import Button from '../components/Button.svelte';
  import CopyButton from '../components/CopyButton.svelte';
  import Spinner from '../components/Spinner.svelte';
  import { describe, safeNotesURL } from './updateState';

  type Load = { state: 'loading' } | { state: 'error'; message: string } | { state: 'ready' };

  let load = $state<Load>({ state: 'loading' });
  let status = $state<UpdateStatus>();
  let checking = $state(false);
  let checkError = $state<string>();

  const messageOf = (err: unknown) => (err instanceof ApiError ? err.message : 'Request failed.');
  // 401 is handled app-wide (redirect to login); the card just stays quiet.
  const isSessionExpiry = (err: unknown) => err instanceof ApiError && err.status === 401;

  const view = $derived(status ? describe(status, new Date()) : undefined);
  const notesURL = $derived(status?.state === 'available' ? safeNotesURL(status.notesURL) : undefined);

  async function loadStatus() {
    load = { state: 'loading' };
    try {
      status = await api.updateStatus();
      load = { state: 'ready' };
    } catch (err) {
      load = { state: 'error', message: isSessionExpiry(err) ? 'Your session has expired.' : messageOf(err) };
    }
  }

  async function checkNow() {
    checking = true;
    checkError = undefined;
    try {
      status = await api.checkUpdate();
    } catch (err) {
      // The previous status stays on screen: a failed request is not a result.
      if (!isSessionExpiry(err)) checkError = messageOf(err);
    } finally {
      checking = false;
    }
  }

  onMount(() => {
    void loadStatus();
  });
</script>

<section class="card" aria-labelledby="update-h">
  <header>
    <h2 id="update-h">Software update</h2>
    {#if view}<span class="pill {view.tone}">{view.pill}</span>{/if}
  </header>
  <div class="body stack">
    {#if load.state === 'loading'}
      <Spinner />
    {:else if load.state === 'error'}
      <Banner kind="danger" title="Could not load the update status">{load.message}</Banner>
      <div><Button variant="secondary" onclick={loadStatus}>Retry</Button></div>
    {:else if status && view}
      {#if checkError}<Banner kind="danger" title="Could not check for updates">{checkError}</Banner>{/if}
      <p class="headline">
        <span>{view.headline}</span>
        {#if notesURL}
          · <a href={notesURL} target="_blank" rel="noopener noreferrer">Release notes</a>
        {/if}
      </p>
      {#if view.detail}<p class="muted">{view.detail}</p>{/if}
      <div class="cluster">
        <pre class="mono" data-testid="upgrade-command">{status.command}</pre>
        <CopyButton text={status.command} label="Copy command" />
      </div>
      {#if status.state === 'available'}
        <p class="muted">
          The service restarts; this page reconnects by itself. Back up the database first
          (<code>stackstead db backup &lt;file&gt;</code>) if you want to be able to roll back.
        </p>
      {/if}
      {#if status.state !== 'disabled'}
        <div><Button variant="secondary" loading={checking} onclick={checkNow}>Check now</Button></div>
      {/if}
    {/if}
  </div>
  <footer>Run the command in a terminal on the host. Nothing is installed from this page.</footer>
</section>

<style>
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); box-shadow: var(--shadow); }
  .card header { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); padding: 10px 14px; border-bottom: 1px solid var(--border); }
  .card h2 { margin: 0; font-size: 13px; font-weight: 600; }
  .body { padding: 12px 14px; font-size: 12.5px; }
  .body p { margin: 0; }
  .card footer { padding: 8px 14px; border-top: 1px solid var(--border); color: var(--faint); font-size: 12px; }
  .headline { font-weight: 600; }
  .muted { color: var(--muted); }
  .pill { font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.02em; color: var(--tone, var(--muted)); }
  .pill.success { --tone: var(--success); }
  .pill.accent { --tone: var(--accent); }
  .pill.danger { --tone: var(--danger); }
  .pill.muted { --tone: var(--muted); }
  pre { overflow-x: auto; margin: 0; padding: var(--space-3); background: var(--bg); border: 1px solid var(--border); border-radius: var(--radius-md); flex: 1; min-width: 0; }
  code { font-family: var(--mono); }
</style>
