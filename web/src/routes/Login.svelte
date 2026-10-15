<script module lang="ts">
  /**
   * Returns `next` only when it is a same-origin path. Anything else (absolute URLs, `//host`,
   * `/\host`, `javascript:`, control characters) falls back to '/', so ?next= cannot be an open redirect.
   */
  export function safeNext(next: string | null): string {
    if (!next || !next.startsWith('/') || next.startsWith('//')) return '/';
    // Backslashes and control chars are treated as '/' or stripped by browsers, so '/\evil' can become '//evil'.
    // eslint-disable-next-line no-control-regex
    if (/[\\\u0000-\u001f\u007f]/.test(next)) return '/';
    try {
      const url = new URL(next, window.location.origin);
      if (url.origin !== window.location.origin) return '/';
      // Normalisation can collapse '/.//evil' into the protocol-relative '//evil'.
      if (url.pathname.startsWith('//') || url.pathname.includes('\\')) return '/';
      return url.pathname + url.search + url.hash;
    } catch {
      return '/';
    }
  }
</script>

<script lang="ts">
  import { ApiError } from '../lib/api/client';
  import { login } from '../lib/api/session';
  import Button from '../lib/components/Button.svelte';
  import Field from '../lib/components/Field.svelte';
  import { navigate, route } from '../lib/router';

  let username = $state('');
  let password = $state('');
  let pending = $state(false);
  let error = $state('');

  $effect(() => {
    document.title = 'Sign in · Stackstead';
  });

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (pending) return;
    pending = true;
    error = '';
    try {
      await login(username, password);
      navigate(safeNext($route.query.get('next')), { replace: true });
    } catch (err) {
      password = '';
      if (err instanceof ApiError && err.status === 401) error = 'invalid username or password';
      else if (err instanceof ApiError && err.status === 429) error = err.message;
      else error = 'Could not sign in. Check your connection and try again.';
    } finally {
      pending = false;
    }
  }
</script>

<div class="auth">
  <form class="card stack" onsubmit={submit}>
    <header class="brand">
      <img src="/logo-art.png" alt="" />
      <h1 class="wordmark"><span class="visually-hidden">Sign in to </span>Stack<b>stead</b></h1>
      <span class="host">{window.location.host}</span>
    </header>
    {#if error}<p role="alert" class="error"><span aria-hidden="true">✖ </span>{error}</p>{/if}
    <Field label="Username" id="login-username">
      {#snippet children(c)}
        <input {...c} type="text" autocomplete="username" required bind:value={username} />
      {/snippet}
    </Field>
    <Field label="Password" id="login-password">
      {#snippet children(c)}
        <input {...c} type="password" autocomplete="current-password" required bind:value={password} />
      {/snippet}
    </Field>
    <Button variant="primary" type="submit" loading={pending}>Sign in</Button>
    <p class="footer">Localhost only. For remote access use a TLS reverse proxy.</p>
  </form>
</div>

<style>
  .auth { min-height: 100vh; display: flex; align-items: center; justify-content: center; padding: 24px; background: var(--bg); }
  .card {
    width: 100%;
    max-width: 360px;
    padding: 28px;
    gap: 16px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius-lg);
    box-shadow: var(--shadow);
  }
  .brand { display: flex; flex-direction: column; align-items: center; gap: 6px; text-align: center; }
  .brand img { width: 168px; height: auto; }
  .wordmark { margin: 0; font-size: 24px; font-weight: 600; }
  .wordmark b { font-weight: 400; }
  .host { color: var(--muted); font-size: 12px; }
  .error { color: var(--danger); overflow-wrap: anywhere; }
  .card :global(input) { height: 34px; }
  .card :global(.btn) { width: 100%; height: 34px; }
  .footer { margin: 0; color: var(--faint); font-size: 11px; text-align: center; }
</style>
