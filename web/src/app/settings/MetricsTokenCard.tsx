import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { api, ApiError } from '$lib/api/client';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { CopyButton } from '../components/CopyButton';
import { Field } from '../components/Field';
import { Spinner } from '../components/Spinner';
import { Pill, SettingsCard } from './SettingsCard';
import styles from './SettingsScreen.module.css';

type Status = { state: 'loading' } | { state: 'error'; message: string } | { state: 'ready'; configured: boolean };

const messageOf = (err: unknown) => (err instanceof ApiError ? err.message : 'Request failed.');

export function MetricsTokenCard() {
  const [status, setStatus] = useState<Status>({ state: 'loading' });
  // The one-time secret lives only in this component's state: never a store, storage or log.
  const [token, setToken] = useState<string | null>(null);
  const [confirming, setConfirming] = useState<'rotate' | 'disable' | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState<string>();
  // A ref, not state: two clicks in one tick both see the same render's `busy`.
  const busyRef = useRef(false);
  const started = useRef(false);
  const tokenInput = useRef<HTMLInputElement>(null);
  const pendingFocus = useRef<'token' | 'rotate' | null>(null);

  const snippet = token
    ? `scrape_configs:\n  - job_name: stackstead\n    metrics_path: /metrics\n    scheme: ${window.location.protocol.slice(0, -1)}\n    authorization:\n      credentials: ${token}\n    static_configs:\n      - targets: ['${window.location.host}']`
    : '';

  async function loadStatus() {
    setStatus({ state: 'loading' });
    try {
      const s = await api.metricsTokenStatus();
      setStatus({ state: 'ready', configured: s.configured });
    } catch (err) {
      setStatus({ state: 'error', message: messageOf(err) });
    }
  }

  async function createToken() {
    setConfirming(null);
    if (busyRef.current) return;
    busyRef.current = true;
    setActionError(undefined);
    setBusy(true);
    try {
      const r = await api.createMetricsToken();
      pendingFocus.current = 'token';
      setToken(r.token);
      setStatus({ state: 'ready', configured: true });
    } catch (err) {
      setActionError(messageOf(err));
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  }

  async function disableToken() {
    setConfirming(null);
    if (busyRef.current) return;
    busyRef.current = true;
    setActionError(undefined);
    setBusy(true);
    try {
      await api.deleteMetricsToken();
      setToken(null);
      setStatus({ state: 'ready', configured: false });
    } catch (err) {
      setActionError(messageOf(err));
    } finally {
      busyRef.current = false;
      setBusy(false);
    }
  }

  function dismissToken() {
    pendingFocus.current = 'rotate';
    setToken(null);
  }

  useEffect(() => {
    if (!started.current) {
      started.current = true; // StrictMode runs effects twice
      void loadStatus();
    }
    return () => setToken(null);
  }, []);

  // The token goes into the input as a property, not a `value` attribute, so it is never serialised into the markup.
  useLayoutEffect(() => {
    if (token !== null && tokenInput.current) tokenInput.current.value = token;
    if (pendingFocus.current === 'token') tokenInput.current?.focus();
    else if (pendingFocus.current === 'rotate') document.getElementById('rotate-token')?.focus();
    pendingFocus.current = null;
  });

  return (
    <>
      <SettingsCard
        id="metrics-h"
        title="Prometheus token"
        headerEnd={
          status.state === 'ready' && (
            <Pill tone={status.configured ? 'success' : 'muted'}>{status.configured ? 'configured' : 'not configured'}</Pill>
          )
        }
        footer="Scraping setup: docs/api.md (section “Prometheus”)."
      >
        <p className={styles.intro}>
          Prometheus scrapes <code>/metrics</code> with this bearer token.
        </p>
        {status.state === 'loading' ? (
          <Spinner />
        ) : status.state === 'error' ? (
          <>
            <Banner kind="danger" title="Could not load the token status">
              {status.message}
            </Banner>
            <div>
              <Button variant="secondary" onClick={loadStatus}>
                Retry
              </Button>
            </div>
          </>
        ) : (
          <>
            {actionError && (
              <Banner kind="danger" title="Token request failed">
                {actionError}
              </Banner>
            )}
            {token !== null ? (
              <>
                <Banner kind="warning" title="Copy this token now">
                  It is shown only once and cannot be retrieved later.
                </Banner>
                <Field label="Metrics token" id="metrics-token" mono>
                  {(control) => (
                    <div className={styles.cluster}>
                      <input
                        {...control}
                        ref={tokenInput}
                        type="text"
                        readOnly
                        autoComplete="off"
                        spellCheck={false}
                        className={styles.tokenInput}
                      />
                      <CopyButton text={token} />
                    </div>
                  )}
                </Field>
                <pre className={styles.snippet} data-testid="scrape-snippet">
                  {snippet}
                </pre>
                <div>
                  <CopyButton text={snippet} label="Copy snippet" />
                </div>
                <div>
                  <Button variant="primary" onClick={dismissToken}>
                    I have saved it
                  </Button>
                </div>
              </>
            ) : status.configured ? (
              <>
                <p>A token is configured.</p>
                <div className={styles.cluster}>
                  <Button variant="secondary" id="rotate-token" loading={busy} onClick={() => setConfirming('rotate')}>
                    Rotate token
                  </Button>
                  <Button variant="danger" loading={busy} onClick={() => setConfirming('disable')}>
                    Disable
                  </Button>
                </div>
              </>
            ) : (
              <>
                <p>
                  No token exists, so <code>/metrics</code> answers 404 until you create one.
                </p>
                <div>
                  <Button variant="primary" loading={busy} onClick={createToken}>
                    Create token
                  </Button>
                </div>
              </>
            )}
          </>
        )}
      </SettingsCard>
      <ConfirmDialog
        open={confirming === 'rotate'}
        title="Rotate token"
        message="Rotating creates a new token. The old token stops working immediately."
        confirmLabel="Rotate"
        danger
        onConfirm={createToken}
        onCancel={() => setConfirming(null)}
      />
      <ConfirmDialog
        open={confirming === 'disable'}
        title="Disable token"
        message="Disabling removes the token. /metrics answers 404 and any Prometheus using it stops scraping."
        confirmLabel="Disable token"
        danger
        onConfirm={disableToken}
        onCancel={() => setConfirming(null)}
      />
    </>
  );
}
