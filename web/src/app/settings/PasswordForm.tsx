import { useEffect, useRef, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import { api, ApiError } from '$lib/api/client';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Field } from '../components/Field';
import styles from './PasswordForm.module.css';

const MIN_LENGTH = 12;
const LOCK_MS = 15 * 60 * 1000;

// Password values live only in component state: never logged, toasted, rendered or persisted.
export function PasswordForm({ actions }: { actions?: ReactNode }) {
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [currentError, setCurrentError] = useState<string>();
  const [newError, setNewError] = useState<string>();
  const [rateLimited, setRateLimited] = useState<string>();
  const busyRef = useRef(false);
  const unlockTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const currentInput = useRef<HTMLInputElement>(null);
  const pendingFocus = useRef(false);

  function unlock() {
    clearTimeout(unlockTimer.current);
    setRateLimited(undefined);
  }
  useEffect(() => () => clearTimeout(unlockTimer.current), []);

  // Focus after the error render has committed.
  useEffect(() => {
    if (currentError && pendingFocus.current) {
      pendingFocus.current = false;
      currentInput.current?.focus();
    }
  }, [currentError]);

  const ready = current !== '' && next !== '' && confirm !== '' && next === confirm;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!ready || busyRef.current || rateLimited) return;
    setCurrentError(undefined);
    setNewError(undefined);
    if ([...next].length < MIN_LENGTH) {
      setNewError(`Use at least ${MIN_LENGTH} characters.`);
      return; // validation error: keep what the user typed
    }
    busyRef.current = true;
    setBusy(true);
    let keepCurrent = false;
    let wrongCurrent = false;
    try {
      await api.changePassword(current, next);
      toasts.push({ kind: 'success', message: 'Password changed. Other sessions were signed out.' });
    } catch (err) {
      const status = err instanceof ApiError ? err.status : 0;
      if (status === 403) {
        wrongCurrent = true;
        setCurrentError('Current password is incorrect.');
      } else if (status === 400) {
        setNewError((err as ApiError).message);
        keepCurrent = true;
      } else if (status === 429) {
        setRateLimited((err as ApiError).message);
        clearTimeout(unlockTimer.current);
        unlockTimer.current = setTimeout(unlock, LOCK_MS);
      } else toasts.push({ kind: 'danger', message: 'Could not change the password. Try again.' });
    } finally {
      const savedCurrent = current;
      setCurrent(keepCurrent ? savedCurrent : '');
      setNext('');
      setConfirm('');
      pendingFocus.current = wrongCurrent;
      busyRef.current = false;
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} autoComplete="off" onSubmit={submit}>
      {rateLimited && (
        <>
          <Banner kind="danger" title="Too many attempts">
            {rateLimited} The form is disabled; try again in about 15 minutes.
          </Banner>
          <div>
            <Button variant="secondary" onClick={unlock}>
              Try again
            </Button>
          </div>
        </>
      )}
      <div className={styles.fields}>
        <Field label="Current password" id="pw-current" error={currentError}>
          {({ readOnly: _, ...c }) => (
            <input {...c} ref={currentInput} type="password" autoComplete="current-password" value={current} disabled={!!rateLimited} onChange={(e) => setCurrent(e.target.value)} />
          )}
        </Field>
        <Field label="New password" id="pw-new" error={newError} hint={`At least ${MIN_LENGTH} characters.`}>
          {({ readOnly: _, ...c }) => (
            <input {...c} type="password" autoComplete="new-password" value={next} disabled={!!rateLimited} onChange={(e) => setNext(e.target.value)} />
          )}
        </Field>
        <Field label="Confirm new password" id="pw-confirm" error={confirm !== '' && next !== confirm ? 'Passwords do not match.' : undefined}>
          {({ readOnly: _, ...c }) => (
            <input {...c} type="password" autoComplete="new-password" value={confirm} disabled={!!rateLimited} onChange={(e) => setConfirm(e.target.value)} />
          )}
        </Field>
      </div>
      <div className={styles.cluster}>
        <Button variant="primary" type="submit" loading={busy} disabled={!ready || !!rateLimited}>
          Change password
        </Button>
        {actions}
      </div>
    </form>
  );
}
