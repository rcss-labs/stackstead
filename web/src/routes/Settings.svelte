<script lang="ts">
  import { api, ApiError } from '../lib/api/client';
  import { logout, session } from '../lib/api/session';
  import Banner from '../lib/components/Banner.svelte';
  import Button from '../lib/components/Button.svelte';
  import ConfirmDialog from '../lib/components/ConfirmDialog.svelte';
  import CopyButton from '../lib/components/CopyButton.svelte';
  import Spinner from '../lib/components/Spinner.svelte';
  import PasswordForm from '../lib/settings/PasswordForm.svelte';
  import { live, refreshStatus } from '../lib/stores/live';
  import { toasts } from '../lib/stores/toasts';

  type Status = { state: 'loading' } | { state: 'error'; message: string } | { state: 'ready'; configured: boolean };

  let status = $state<Status>({ state: 'loading' });
  // The one-time secret lives only in this component's state: never a store or storage.
  let token = $state<string | null>(null);
  let confirming = $state<'rotate' | 'disable' | null>(null);
  let busy = $state(false);
  let actionError = $state<string>();

  const snippet = $derived(
    token
      ? `scrape_configs:\n  - job_name: stackstead\n    metrics_path: /metrics\n    authorization:\n      credentials: ${token}\n    static_configs:\n      - targets: ['${window.location.host}']`
      : '',
  );

  const messageOf = (err: unknown) => (err instanceof ApiError ? err.message : 'Request failed.');

  async function loadStatus() {
    status = { state: 'loading' };
    try {
      const s = await api.metricsTokenStatus();
      status = { state: 'ready', configured: s.configured };
    } catch (err) {
      status = { state: 'error', message: messageOf(err) };
    }
  }

  async function createToken() {
    confirming = null;
    actionError = undefined;
    busy = true;
    try {
      const r = await api.createMetricsToken();
      token = r.token;
      status = { state: 'ready', configured: true };
    } catch (err) {
      actionError = messageOf(err);
    } finally {
      busy = false;
    }
  }

  async function disableToken() {
    confirming = null;
    actionError = undefined;
    busy = true;
    try {
      await api.deleteMetricsToken();
      token = null;
      status = { state: 'ready', configured: false };
    } catch (err) {
      actionError = messageOf(err);
    } finally {
      busy = false;
    }
  }

  async function doLogout() {
    const { ok } = await logout();
    if (!ok) toasts.push({ kind: 'warning', message: 'Logout may not have completed on the server.' });
  }

  $effect(() => {
    void loadStatus();
    return () => {
      token = null;
    };
  });
</script>

<section class="stack">
  <h1>Settings</h1>

  <section class="stack" aria-labelledby="metrics-h">
    <h2 id="metrics-h">Prometheus token</h2>
    {#if status.state === 'loading'}
      <Spinner />
    {:else if status.state === 'error'}
      <Banner kind="danger" title="Could not load the token status">
        {status.message}
      </Banner>
      <div><Button variant="secondary" onclick={loadStatus}>Retry</Button></div>
    {:else}
      {#if actionError}<Banner kind="danger" title="Token request failed">{actionError}</Banner>{/if}
      {#if token}
        <div class="stack">
          <Banner kind="warning" title="Copy this token now">It is shown only once and cannot be retrieved later.</Banner>
          <label for="metrics-token">Metrics token</label>
          <div class="cluster">
            <input id="metrics-token" type="text" readonly value={token} autocomplete="off" />
            <CopyButton text={token} />
          </div>
          <pre data-testid="scrape-snippet">{snippet}</pre>
          <CopyButton text={snippet} label="Copy snippet" />
          <div><Button variant="primary" onclick={() => (token = null)}>I have saved it</Button></div>
        </div>
      {:else if status.configured}
        <p>A token is configured.</p>
        <div class="cluster">
          <Button variant="secondary" loading={busy} onclick={() => (confirming = 'rotate')}>Rotate token</Button>
          <Button variant="danger" loading={busy} onclick={() => (confirming = 'disable')}>Disable</Button>
        </div>
      {:else}
        <p>No token exists, so <code>/metrics</code> answers 404 until you create one.</p>
        <div><Button variant="primary" loading={busy} onclick={createToken}>Create token</Button></div>
      {/if}
    {/if}
  </section>

  <section class="stack" aria-labelledby="status-h">
    <h2 id="status-h">System status</h2>
    <dl>
      <dt>Docker</dt><dd data-testid="status-docker">{$live.docker}</dd>
      <dt>Compose</dt><dd data-testid="status-compose">{$live.compose}</dd>
      <dt>Events</dt><dd data-testid="status-events">{$live.events}</dd>
      <dt>Metrics</dt><dd data-testid="status-metrics">{$live.metricsDegraded ? 'degraded' : 'ok'}</dd>
    </dl>
    <div><Button variant="secondary" onclick={() => void refreshStatus()}>Refresh</Button></div>
  </section>

  <section class="stack" aria-labelledby="account-h">
    <h2 id="account-h">Account</h2>
    <p>Signed in as <strong>{$session.username ?? 'unknown'}</strong></p>
    <div><Button variant="secondary" onclick={doLogout}>Log out</Button></div>
    <h3>Change password</h3>
    <PasswordForm />
  </section>

  <section class="stack" aria-labelledby="about-h">
    <h2 id="about-h">About</h2>
    <p>
      See the README sections “Prometheus”, for reverse-proxy setup, and “Security model” for how Stackstead protects
      access.
    </p>
  </section>
</section>

<ConfirmDialog
  open={confirming === 'rotate'}
  title="Rotate token"
  message="Rotating creates a new token. The old token stops working immediately."
  confirmLabel="Rotate"
  danger
  onconfirm={createToken}
  oncancel={() => (confirming = null)}
/>
<ConfirmDialog
  open={confirming === 'disable'}
  title="Disable token"
  message="Disabling removes the token. /metrics answers 404 and any Prometheus using it stops scraping."
  confirmLabel="Disable token"
  danger
  onconfirm={disableToken}
  oncancel={() => (confirming = null)}
/>

<style>
  input[readonly] { flex: 1; min-width: 0; font-family: var(--font-mono, monospace); }
  pre { overflow-x: auto; padding: var(--space-3); background: var(--surface-raised); border-radius: var(--radius-md); }
  dl { display: grid; grid-template-columns: max-content 1fr; gap: var(--space-1) var(--space-4); }
  dt { font-weight: 600; }
</style>
