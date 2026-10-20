import { useEffect, useRef, useState } from 'react';
import { api, ApiError } from '$lib/api/client';
import type { UpdateStatus } from '$lib/api/types';
import { describe, safeNotesURL } from '$lib/settings/updateState';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { CopyButton } from '../components/CopyButton';
import { Spinner } from '../components/Spinner';
import { Pill, SettingsCard } from './SettingsCard';
import styles from './SettingsCard.module.css';

type Load = { state: 'loading' } | { state: 'error'; message: string } | { state: 'ready' };

const messageOf = (err: unknown) => (err instanceof ApiError ? err.message : 'Request failed.');
// 401 is handled app-wide (redirect to login); the card just stays quiet.
const isSessionExpiry = (err: unknown) => err instanceof ApiError && err.status === 401;

export function UpdateCard() {
  const [load, setLoad] = useState<Load>({ state: 'loading' });
  const [status, setStatus] = useState<UpdateStatus>();
  const [checking, setChecking] = useState(false);
  const [checkError, setCheckError] = useState<string>();
  const started = useRef(false);

  const view = status ? describe(status, new Date()) : undefined;
  const notesURL = status?.state === 'available' ? safeNotesURL(status.notesURL) : undefined;

  async function loadStatus() {
    setLoad({ state: 'loading' });
    try {
      setStatus(await api.updateStatus());
      setLoad({ state: 'ready' });
    } catch (err) {
      setLoad({ state: 'error', message: isSessionExpiry(err) ? 'Your session has expired.' : messageOf(err) });
    }
  }

  async function checkNow() {
    setChecking(true);
    setCheckError(undefined);
    try {
      setStatus(await api.checkUpdate());
    } catch (err) {
      // The previous status stays on screen: a failed request is not a result.
      if (!isSessionExpiry(err)) setCheckError(messageOf(err));
    } finally {
      setChecking(false);
    }
  }

  useEffect(() => {
    if (started.current) return; // StrictMode runs effects twice
    started.current = true;
    void loadStatus();
  }, []);

  return (
    <SettingsCard
      id="update-h"
      title="Software update"
      headerEnd={view && <Pill tone={view.tone}>{view.pill}</Pill>}
      footer="Run the command in a terminal on the host. Nothing is installed from this page."
    >
      {load.state === 'loading' ? (
        <Spinner />
      ) : load.state === 'error' ? (
        <>
          <Banner kind="danger" title="Could not load the update status">
            {load.message}
          </Banner>
          <div>
            <Button variant="secondary" onClick={loadStatus}>
              Retry
            </Button>
          </div>
        </>
      ) : status && view ? (
        <>
          {checkError && (
            <Banner kind="danger" title="Could not check for updates">
              {checkError}
            </Banner>
          )}
          <p className={styles.headline}>
            <span>{view.headline}</span>
            {notesURL && (
              <>
                {' · '}
                <a href={notesURL} target="_blank" rel="noopener noreferrer">
                  Release notes
                </a>
              </>
            )}
          </p>
          {view.detail && <p className={styles.muted}>{view.detail}</p>}
          <div className={styles.cluster}>
            <pre className={styles.command} data-testid="upgrade-command">
              {status.command}
            </pre>
            <CopyButton text={status.command} label="Copy command" />
          </div>
          {status.state === 'available' && (
            <p className={styles.muted}>
              The service restarts; this page reconnects by itself. Back up the database first (<code className={styles.code}>stackstead db backup &lt;file&gt;</code>) if you want to be able to roll back.
            </p>
          )}
          {status.state !== 'disabled' && (
            <div>
              <Button variant="secondary" loading={checking} onClick={checkNow}>
                Check now
              </Button>
            </div>
          )}
        </>
      ) : null}
    </SettingsCard>
  );
}
