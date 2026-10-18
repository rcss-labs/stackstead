<script lang="ts">
  import { tick } from 'svelte';
  import { api, ApiError } from '../lib/api/client';
  import { logout, session } from '../lib/api/session';
  import Banner from '../lib/components/Banner.svelte';
  import Button from '../lib/components/Button.svelte';
  import ConfirmDialog from '../lib/components/ConfirmDialog.svelte';
  import CopyButton from '../lib/components/CopyButton.svelte';
  import Spinner from '../lib/components/Spinner.svelte';
  import PasswordForm from '../lib/settings/PasswordForm.svelte';
  import UpdateCard from '../lib/settings/UpdateCard.svelte';
  import { density, setDensity, setTheme, theme, type Density, type ThemeChoice } from '../lib/stores/appearance';
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
      ? `scrape_configs:\n  - job_name: stackstead\n    metrics_path: /metrics\n    scheme: ${window.location.protocol.slice(0, -1)}\n    authorization:\n      credentials: ${token}\n    static_configs:\n      - targets: ['${window.location.host}']`
      : '',
  );

  let tokenInput = $state<HTMLInputElement>();
  $effect(() => {
    tokenInput?.focus();
  });

  async function dismissToken() {
    token = null;
    await tick();
    document.getElementById('rotate-token')?.focus();
  }

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

  const themeOptions: { value: ThemeChoice; label: string }[] = [
    { value: 'dark', label: 'Dark' },
    { value: 'light', label: 'Light' },
    { value: 'system', label: 'System' },
  ];
  const densityOptions: { value: Density; label: string }[] = [
    { value: 'compact', label: 'Compact' },
    { value: 'comfortable', label: 'Comfortable' },
  ];

  // Only an explicit 'unavailable' means down; 'unknown' (no answer yet) stays neutral.
  const dockerText = $derived(
    $live.docker === 'ok' ? ($live.dockerVersion ? `${$live.dockerVersion} · ok` : 'ok') : $live.docker,
  );
  const dockerTone = $derived($live.docker === 'ok' ? 'success' : $live.docker === 'unavailable' ? 'danger' : 'muted');
  const composeTone = $derived($live.compose === 'unknown' || $live.compose === 'unavailable' ? 'muted' : 'success');
  const eventsTone = $derived($live.events === 'connected' ? 'success' : $live.events === 'disconnected' ? 'danger' : 'muted');
  const metricsText = $derived(
    $live.metricsDegraded
      ? 'degraded'
      : $live.metricsIntervalSeconds
        ? `ok · ${$live.metricsIntervalSeconds} s`
        : 'ok',
  );

  $effect(() => {
    void loadStatus();
    return () => {
      token = null;
    };
  });
</script>

<section class="page">
  <h1>Settings</h1>

  <section class="card" aria-labelledby="status-h">
    <header><h2 id="status-h">System status</h2></header>
    <div class="body stack">
      <dl class="grid status">
        <div><dt>Docker</dt><dd data-testid="status-docker" class="mono {dockerTone}"><span class="dot"></span>{dockerText}</dd></div>
        <div><dt>Compose</dt><dd data-testid="status-compose" class="mono {composeTone}"><span class="dot"></span>{$live.compose}</dd></div>
        <div><dt>Events</dt><dd data-testid="status-events" class="mono {eventsTone}"><span class="dot"></span>{$live.events}</dd></div>
        <div><dt>Metrics</dt><dd data-testid="status-metrics" class="mono {$live.metricsDegraded ? 'warning' : 'success'}"><span class="dot"></span>{metricsText}</dd></div>
      </dl>
      <div><Button variant="secondary" size="sm" onclick={() => void refreshStatus()}>Refresh</Button></div>
    </div>
  </section>

  <UpdateCard />

  <section class="card" aria-labelledby="metrics-h">
    <header>
      <h2 id="metrics-h">Prometheus token</h2>
      {#if status.state === 'ready'}
        <span class="pill" class:on={status.configured}>{status.configured ? 'configured' : 'not configured'}</span>
      {/if}
    </header>
    <div class="body stack">
      <p class="intro">Prometheus scrapes <code>/metrics</code> with this bearer token.</p>
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
            <input id="metrics-token" bind:this={tokenInput} type="text" readonly value={token} autocomplete="off" />
            <CopyButton text={token} />
          </div>
          <pre data-testid="scrape-snippet">{snippet}</pre>
          <CopyButton text={snippet} label="Copy snippet" />
          <div><Button variant="primary" onclick={dismissToken}>I have saved it</Button></div>
        </div>
      {:else if status.configured}
        <p>A token is configured.</p>
        <div class="cluster">
          <Button variant="secondary" id="rotate-token" loading={busy} onclick={() => (confirming = 'rotate')}>Rotate token</Button>
          <Button variant="danger" loading={busy} onclick={() => (confirming = 'disable')}>Disable</Button>
        </div>
      {:else}
        <p>No token exists, so <code>/metrics</code> answers 404 until you create one.</p>
        <div><Button variant="primary" loading={busy} onclick={createToken}>Create token</Button></div>
      {/if}
    {/if}
    </div>
    <footer>Scraping setup: docs/api.md (section “Prometheus”).</footer>
  </section>

  <section class="card" aria-labelledby="appearance-h">
    <header><h2 id="appearance-h">Appearance</h2></header>
    <div class="body grid appearance">
      <div class="stack">
        <span class="label" id="theme-label">Theme</span>
        <div class="segmented" role="group" aria-labelledby="theme-label">
          {#each themeOptions as o (o.value)}
            <button type="button" aria-pressed={$theme === o.value} onclick={() => setTheme(o.value)}>{o.label}</button>
          {/each}
        </div>
      </div>
      <div class="stack">
        <span class="label" id="density-label">Density</span>
        <div class="segmented" role="group" aria-labelledby="density-label">
          {#each densityOptions as o (o.value)}
            <button type="button" aria-pressed={$density === o.value} onclick={() => setDensity(o.value)}>{o.label}</button>
          {/each}
        </div>
      </div>
    </div>
  </section>

  <section class="card" aria-labelledby="account-h">
    <header><h2 id="account-h">Account</h2></header>
    <div class="body stack">
      <p>Signed in as <strong>{$session.username ?? 'unknown'}</strong>. Changing the password signs out other sessions.</p>
      <PasswordForm>
        {#snippet actions()}
          <Button variant="secondary" onclick={doLogout}>Log out</Button>
        {/snippet}
      </PasswordForm>
    </div>
    <footer>Remote access and hardening: docs/security.md.</footer>
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
  .page { display: flex; flex-direction: column; gap: var(--space-4); max-width: 720px; }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); box-shadow: var(--shadow); }
  .card header { display: flex; align-items: center; justify-content: space-between; gap: var(--space-3); padding: 10px 14px; border-bottom: 1px solid var(--border); }
  .card h2 { margin: 0; font-size: 13px; font-weight: 600; }
  .body { padding: 12px 14px; font-size: 12.5px; }
  .body p { margin: 0; }
  .card footer { padding: 8px 14px; border-top: 1px solid var(--border); color: var(--faint); font-size: 12px; }
  .intro { color: var(--muted); }
  .intro code { font-family: var(--mono); }
  .pill { font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.02em; color: var(--muted); }
  .pill.on { color: var(--success); }
  .grid { display: grid; gap: var(--space-3) var(--space-4); }
  .status { grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); margin: 0; }
  .appearance { grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); }
  dt, .label { color: var(--muted); font-size: 11px; text-transform: uppercase; letter-spacing: 0.02em; }
  dd { margin: 2px 0 0; min-width: 0; overflow-wrap: anywhere; display: flex; align-items: center; gap: 7px; }
  .mono { font-family: var(--mono); }
  .dot { width: 7px; height: 7px; border-radius: 50%; background: var(--tone, var(--muted)); flex: none; }
  .success { --tone: var(--success); }
  .warning { --tone: var(--warning); }
  .danger { --tone: var(--danger); }
  .muted { --tone: var(--muted); }
  .segmented { display: inline-flex; }
  .segmented button {
    height: 26px; padding: 0 12px; font: inherit; font-size: 12px; color: var(--text);
    background: var(--surface); border: 1px solid var(--border-strong); margin-left: -1px; cursor: pointer;
  }
  .segmented button:first-child { margin-left: 0; border-radius: var(--radius) 0 0 var(--radius); }
  .segmented button:last-child { border-radius: 0 var(--radius) var(--radius) 0; }
  .segmented button:hover { background: var(--hover); }
  .segmented button[aria-pressed='true'] { background: var(--accent-soft); color: var(--accent); border-color: var(--accent); position: relative; font-weight: 600; }
  input[readonly] { flex: 1; min-width: 0; font-family: var(--mono, monospace); }
  pre { overflow-x: auto; margin: 0; padding: var(--space-3); background: var(--bg); border: 1px solid var(--border); border-radius: var(--radius-md); }
</style>
