<script lang="ts">
  import { tick, type Snippet } from 'svelte';
  import { api, ApiError } from '../api/client';
  import Banner from '../components/Banner.svelte';
  import Button from '../components/Button.svelte';
  import Field from '../components/Field.svelte';
  import { toasts } from '../stores/toasts';

  let { actions }: { actions?: Snippet } = $props();

  const MIN_LENGTH = 12;

  let current = $state('');
  let next = $state('');
  let confirm = $state('');
  let busy = $state(false);
  let currentError = $state<string>();
  let newError = $state<string>();
  let rateLimited = $state<string>();
  let currentInput = $state<HTMLInputElement>();
  let unlockTimer: ReturnType<typeof setTimeout> | undefined;
  const LOCK_MS = 15 * 60 * 1000;

  function unlock() {
    clearTimeout(unlockTimer);
    rateLimited = undefined;
  }
  $effect(() => () => clearTimeout(unlockTimer));

  const ready = $derived(current !== '' && next !== '' && confirm !== '' && next === confirm);

  function clearFields() {
    current = '';
    next = '';
    confirm = '';
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (!ready || busy || rateLimited) return;
    currentError = undefined;
    newError = undefined;
    if ([...next].length < MIN_LENGTH) {
      newError = `Use at least ${MIN_LENGTH} characters.`;
      return; // validation error: keep what the user typed
    }
    busy = true;
    let keepCurrent = false;
    try {
      await api.changePassword(current, next);
      toasts.push({ kind: 'success', message: 'Password changed. Other sessions were signed out.' });
    } catch (err) {
      const status = err instanceof ApiError ? err.status : 0;
      if (status === 403) currentError = 'Current password is incorrect.';
      else if (status === 400) {
        newError = (err as ApiError).message;
        keepCurrent = true;
      } else if (status === 429) {
        rateLimited = (err as ApiError).message;
        clearTimeout(unlockTimer);
        unlockTimer = setTimeout(unlock, LOCK_MS);
      }
      else toasts.push({ kind: 'danger', message: 'Could not change the password. Try again.' });
    } finally {
      const savedCurrent = current;
      clearFields();
      if (keepCurrent) current = savedCurrent;
      busy = false;
    }
    if (currentError) {
      await tick();
      currentInput?.focus();
    }
  }
</script>

<form onsubmit={submit} class="stack" autocomplete="off">
  {#if rateLimited}
    <Banner kind="danger" title="Too many attempts">
      {rateLimited} The form is disabled; try again in about 15 minutes.
    </Banner>
    <div><Button variant="secondary" onclick={unlock}>Try again</Button></div>
  {/if}
  <div class="fields">
  <Field label="Current password" id="pw-current" error={currentError}>
    {#snippet children(c)}
      <input type="password" autocomplete="current-password" bind:value={current} bind:this={currentInput} disabled={Boolean(rateLimited)} {...c} />
    {/snippet}
  </Field>
  <Field label="New password" id="pw-new" error={newError} hint="At least {MIN_LENGTH} characters.">
    {#snippet children(c)}
      <input type="password" autocomplete="new-password" bind:value={next} disabled={Boolean(rateLimited)} {...c} />
    {/snippet}
  </Field>
  <Field label="Confirm new password" id="pw-confirm" error={confirm !== '' && next !== confirm ? 'Passwords do not match.' : undefined}>
    {#snippet children(c)}
      <input type="password" autocomplete="new-password" bind:value={confirm} disabled={Boolean(rateLimited)} {...c} />
    {/snippet}
  </Field>
  </div>
  <div class="cluster">
    <Button variant="primary" type="submit" loading={busy} disabled={!ready || Boolean(rateLimited)}>Change password</Button>
    {@render actions?.()}
  </div>
</form>

<style>
  .fields { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: var(--space-3); align-items: start; }
</style>
