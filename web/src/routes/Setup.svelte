<script lang="ts">
  import { tick } from 'svelte';
  import { api, ApiError } from '../lib/api/client';
  import { expireSession, login, session } from '../lib/api/session';
  import { toasts } from '../lib/stores/toasts';
  import Button from '../lib/components/Button.svelte';
  import Field from '../lib/components/Field.svelte';
  import { navigate } from '../lib/router';

  /** Mirrors the server's MinPasswordLength. */
  const MIN_PASSWORD = 12;

  let username = $state('');
  let password = $state('');
  let confirm = $state('');
  let token = $state('');
  let pending = $state(false);
  let error = $state('');
  let submitted = $state(false);

  // Count characters (code points), matching the server, not UTF-16 units.
  const passwordTooShort = $derived([...password].length < MIN_PASSWORD);
  const passwordError = $derived(
    submitted && passwordTooShort ? `Password must be at least ${MIN_PASSWORD} characters.` : '',
  );
  const confirmError = $derived(submitted && password !== confirm ? 'Passwords do not match.' : '');

  $effect(() => {
    document.title = 'Set up · Stackstead';
  });

  // Someone else finished setup while this page was open.
  $effect(() => {
    if ($session.state === 'anonymous') navigate('/login', { replace: true });
  });

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (pending) return;
    submitted = true;
    error = '';
    if (passwordTooShort || password !== confirm) {
      await tick();
      document.getElementById(passwordTooShort ? 'setup-password' : 'setup-confirm')?.focus();
      return;
    }
    pending = true;
    let created = false;
    try {
      await api.setup(username, password, token.trim() || undefined);
      created = true;
      await login(username, password);
      navigate('/', { replace: true });
    } catch (err) {
      if (created) {
        toasts.push({ kind: 'warning', message: 'Your account was created, but signing in failed. Please sign in.' });
        expireSession();
        navigate('/login', { replace: true });
        return;
      }
      if (err instanceof ApiError && err.status === 409) {
        toasts.push({ kind: 'info', message: 'Setup is already complete — sign in' });
        expireSession();
        navigate('/login', { replace: true });
        return;
      }
      password = '';
      confirm = '';
      submitted = false;
      if (err instanceof ApiError && err.status === 403) {
        error = 'A setup token is required unless you open this page from the machine itself. Enter the token shown in the server log.';
      } else if (err instanceof ApiError && err.status === 400) {
        error = err.message;
      } else if (err instanceof ApiError && err.status > 0) {
        error = err.message;
      } else {
        error = 'Could not complete setup. Check your connection and try again.';
      }
    } finally {
      pending = false;
    }
  }
</script>

<div class="auth">
  <form class="card stack" onsubmit={submit} novalidate>
    <h1>Set up Stackstead</h1>
    <p class="hint">Create the first account.</p>
    {#if error}<p role="alert" class="error"><span aria-hidden="true">✖ </span>{error}</p>{/if}
    <Field label="Username" id="setup-username">
      {#snippet children(c)}
        <input {...c} type="text" autocomplete="username" required bind:value={username} />
      {/snippet}
    </Field>
    <Field
      label="Password"
      id="setup-password"
      hint={`Use at least ${MIN_PASSWORD} characters.`}
      error={passwordError}
    >
      {#snippet children(c)}
        <input {...c} type="password" autocomplete="new-password" required bind:value={password} />
      {/snippet}
    </Field>
    <Field label="Confirm password" id="setup-confirm" error={confirmError}>
      {#snippet children(c)}
        <input {...c} type="password" autocomplete="new-password" required bind:value={confirm} />
      {/snippet}
    </Field>
    <Field
      label="Setup token (optional)"
      id="setup-token"
      hint="Needed unless you open this page from the machine itself."
    >
      {#snippet children(c)}
        <input {...c} type="text" autocomplete="off" spellcheck="false" autocapitalize="off" bind:value={token} />
      {/snippet}
    </Field>
    <Button variant="primary" type="submit" loading={pending}>Create account</Button>
  </form>
</div>

<style>
  .auth { display: flex; justify-content: center; padding-block: var(--space-6, 2rem); }
  .card {
    width: 100%;
    max-width: 26rem;
    padding: var(--space-5, 1.5rem);
    background: var(--raised);
    border: 1px solid var(--border);
    border-radius: var(--radius-md);
  }
  .hint { color: var(--muted); }
  .error { color: var(--danger); overflow-wrap: anywhere; }
</style>
