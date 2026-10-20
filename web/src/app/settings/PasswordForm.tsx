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

// Password inputs are uncontrolled: the typed values live only in the DOM inputs (read through refs at
// submit), never in React state or a value attribute, and are never logged, toasted, rendered or persisted.
// State holds only derived booleans.
export function PasswordForm({ actions }: { actions?: ReactNode }) {
  const [filled, setFilled] = useState({ current: false, next: false, confirm: false, matches: true });
  const [busy, setBusy] = useState(false);
  const [currentError, setCurrentError] = useState<string>();
  const [newError, setNewError] = useState<string>();
  const [rateLimited, setRateLimited] = useState<string>();
  const busyRef = useRef(false);
  const unlockTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const currentInput = useRef<HTMLInputElement>(null);
  const nextInput = useRef<HTMLInputElement>(null);
  const confirmInput = useRef<HTMLInputElement>(null);
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

  // Derive the booleans from the inputs without keeping their values.
  function syncFilled() {
    const c = currentInput.current?.value ?? '';
    const n = nextInput.current?.value ?? '';
    const k = confirmInput.current?.value ?? '';
    setFilled({ current: c !== '', next: n !== '', confirm: k !== '', matches: n === k });
  }

  const ready = filled.current && filled.next && filled.confirm && filled.matches;

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (!ready || busyRef.current || rateLimited) return;
    setCurrentError(undefined);
    setNewError(undefined);
    const current = currentInput.current?.value ?? '';
    const next = nextInput.current?.value ?? '';
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
      if (currentInput.current && !keepCurrent) currentInput.current.value = '';
      if (nextInput.current) nextInput.current.value = '';
      if (confirmInput.current) confirmInput.current.value = '';
      syncFilled();
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
            <input {...c} ref={currentInput} type="password" autoComplete="current-password" disabled={!!rateLimited} onChange={syncFilled} />
          )}
        </Field>
        <Field label="New password" id="pw-new" error={newError} hint={`At least ${MIN_LENGTH} characters.`}>
          {({ readOnly: _, ...c }) => (
            <input {...c} ref={nextInput} type="password" autoComplete="new-password" disabled={!!rateLimited} onChange={syncFilled} />
          )}
        </Field>
        <Field label="Confirm new password" id="pw-confirm" error={filled.confirm && !filled.matches ? 'Passwords do not match.' : undefined}>
          {({ readOnly: _, ...c }) => (
            <input {...c} ref={confirmInput} type="password" autoComplete="new-password" disabled={!!rateLimited} onChange={syncFilled} />
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
