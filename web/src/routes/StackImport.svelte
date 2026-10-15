<script lang="ts">
  import { tick } from 'svelte';
  import { api, ApiError } from '../lib/api/client';
  import Banner from '../lib/components/Banner.svelte';
  import Button from '../lib/components/Button.svelte';
  import Field from '../lib/components/Field.svelte';
  import { link, navigate } from '../lib/router';
  import FileBrowser from '../lib/stacks/FileBrowser.svelte';
  import { refreshRunningOps, refreshStacks } from '../lib/stores/stacks';
  import { toasts } from '../lib/stores/toasts';

  let path = $state('');
  let pathError = $state('');
  let composeError = $state('');
  let unavailable = $state('');
  let pending = $state(false);
  let browsing = $state(false);

  $effect(() => {
    document.title = 'Import stack · Stackstead';
  });

  const focusPath = async () => {
    await tick();
    const target = composeError ? document.querySelector<HTMLElement>('.compose-error') : document.getElementById('import-path');
    target?.focus();
  };

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (pending) return;
    pathError = composeError = unavailable = '';
    const trimmed = path.trim();
    if (!trimmed) {
      pathError = 'Enter the absolute path of a compose file.';
      await focusPath();
      return;
    }
    pending = true;
    try {
      const { data, checksPartial } = await api.importStack(trimmed);
      void refreshStacks();
      refreshRunningOps();
      toasts.push({
        kind: checksPartial ? 'warning' : 'success',
        message: checksPartial
          ? `Stack ${data.name} imported, but port/IP conflict checks could not run (Docker unreachable).`
          : `Stack ${data.name} imported`,
      });
      navigate(`/stacks/${encodeURIComponent(data.name)}`);
    } catch (err) {
      if (!(err instanceof ApiError)) pathError = 'Could not reach the server. Check your connection and try again.';
      else if (err.status === 422) composeError = err.message;
      else if (err.status === 503) unavailable = err.message;
      else if (err.status === 400 && err.fields.length > 0) pathError = err.fields.map((f) => f.message).join('; ');
      else pathError = err.message;
      await focusPath();
    } finally {
      pending = false;
    }
  }
</script>

<div class="page">
  <h1>Import stack</h1>
  <p class="lead">Register a compose file where it already lives. Nothing is moved or changed until you edit it.</p>
  {#if unavailable}
    <Banner kind="danger" title="Docker/compose unavailable: the stack was not imported">{unavailable}</Banner>
  {/if}
  <form class="card" onsubmit={submit} novalidate>
    <div class="body">
      <Field
        label="Compose file path"
        id="import-path"
        mono
        hint="Absolute path. Browse lists the allowed directories only; the path may also point outside them."
        error={pathError}
      >
        {#snippet children(c)}
          <div class="inline">
            <input {...c} type="text" bind:value={path} autocomplete="off" spellcheck="false" autocapitalize="off" />
            <Button variant="secondary" aria-label="Browse for compose file" onclick={() => (browsing = true)}>Browse…</Button>
          </div>
        {/snippet}
      </Field>
      {#if composeError}
        <div class="compose-error" role="alert" tabindex="-1">
          <div class="head"><span class="what">Compose file</span><span class="verdict">cannot import</span></div>
          <pre>{composeError}</pre>
        </div>
      {/if}
    </div>
    <div class="footer">
      <a href="/" use:link>Cancel</a>
      <Button variant="primary" type="submit" loading={pending}>Import stack</Button>
    </div>
  </form>
</div>

<FileBrowser
  open={browsing}
  mode="file"
  title="Choose a compose file"
  filter={(e) => /\.ya?ml$/.test(e.name)}
  onpick={(p) => {
    path = p;
    browsing = false;
  }}
  onclose={() => (browsing = false)}
/>

<style>
  .page { max-width: 640px; display: flex; flex-direction: column; gap: 12px; }
  .lead { color: var(--muted); font-size: 13px; }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); }
  .body { padding: 14px; display: flex; flex-direction: column; gap: 14px; }
  .inline { display: flex; gap: 8px; }
  .inline input { flex: 1; min-width: 0; }
  .compose-error { background: var(--bg); border: 1px solid var(--border); border-radius: var(--radius); padding: 10px 12px; font-size: 12px; }
  .head { display: flex; justify-content: space-between; gap: 8px; }
  .what { color: var(--muted); }
  .verdict { color: var(--danger); }
  pre { margin: 6px 0 0; font-family: var(--mono); white-space: pre-wrap; overflow-wrap: anywhere; }
  .footer { display: flex; justify-content: flex-end; align-items: center; gap: 14px; padding: 12px 14px; border-top: 1px solid var(--border); }
  .footer a { color: var(--muted); font-size: 13px; }
</style>
