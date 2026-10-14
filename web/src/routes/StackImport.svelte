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

<main class="container">
  <h1>Import stack</h1>
  {#if unavailable}
    <Banner kind="danger" title="Docker/compose unavailable: the stack was not imported">{unavailable}</Banner>
  {/if}
  <form class="stack" onsubmit={submit} novalidate>
    <p>Register an existing compose file. The file is not modified until you edit it.</p>
    <Field
      label="Compose file path"
      id="import-path"
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
      <div class="compose-error" role="alert" tabindex="-1">{composeError}</div>
    {/if}
    <div class="actions">
      <Button variant="primary" type="submit" loading={pending}>Import stack</Button>
      <a href="/" use:link>Cancel</a>
    </div>
  </form>
</main>

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
  .container { max-width: 40rem; padding-block: var(--space-5, 1.5rem); }
  .stack { display: flex; flex-direction: column; gap: var(--space-4); }
  .inline { display: flex; gap: var(--space-2); }
  .inline input { flex: 1; min-width: 0; }
  .compose-error { color: var(--danger); border: 1px solid var(--danger); border-radius: var(--radius-md); padding: var(--space-3); white-space: pre-wrap; overflow-wrap: anywhere; }
  .actions { display: flex; gap: var(--space-4); align-items: center; }
</style>
