import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { useNavigate } from 'react-router';
import { api, ApiError } from '$lib/api/client';
import { expireSession, login, session } from '$lib/api/session';
import { toasts } from '$lib/stores/toasts';
import { Button } from '../components/Button';
import { Field } from '../components/Field';
import { Segmented } from '../components/Segmented';
import { useStore } from '../hooks/useStore';
import { AuthCard, AuthError, AuthHint } from './AuthCard';
import { RestoreFromBackup } from './RestoreFromBackup';

const SETUP_CHOICES = [
  { value: 'admin', label: 'Create admin' },
  { value: 'restore', label: 'Restore from backup' },
];

/** Mirrors the server's MinPasswordLength. */
const MIN_PASSWORD = 12;

// Password and setup-token inputs are uncontrolled: values live only in the DOM inputs and are read through refs at
// submit, never in state, a value attribute, a message, storage or the URL. State holds derived
// booleans (length, match) only.
export function SetupScreen() {
  const navigate = useNavigate();
  const { state: sessionState } = useStore(session);
  const [mode, setMode] = useState<'admin' | 'restore'>('admin');
  const [username, setUsername] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState('');
  const [submitted, setSubmitted] = useState(false);
  // Derived from the inputs on each change; count characters (code points), matching the server.
  const [tooShort, setTooShort] = useState(true);
  const [mismatch, setMismatch] = useState(false);
  const [pendingFocus, setPendingFocus] = useState<'setup-password' | 'setup-confirm' | null>(null);
  const pendingRef = useRef(false);
  const passwordInput = useRef<HTMLInputElement>(null);
  const confirmInput = useRef<HTMLInputElement>(null);
  const tokenInput = useRef<HTMLInputElement>(null);

  const passwordError = submitted && tooShort ? `Password must be at least ${MIN_PASSWORD} characters.` : '';
  const confirmError = submitted && mismatch ? 'Passwords do not match.' : '';

  function syncDerived() {
    const password = passwordInput.current?.value ?? '';
    setTooShort([...password].length < MIN_PASSWORD);
    setMismatch(password !== (confirmInput.current?.value ?? ''));
  }

  useEffect(() => {
    document.title = 'Set up · Stackstead';
  }, []);

  // Someone else finished setup while this page was open.
  useEffect(() => {
    if (sessionState === 'anonymous') navigate('/login', { replace: true });
  }, [sessionState, navigate]);

  useLayoutEffect(() => {
    if (!pendingFocus) return;
    document.getElementById(pendingFocus)?.focus();
    setPendingFocus(null);
  }, [pendingFocus]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pendingRef.current) return;
    const password = passwordInput.current?.value ?? '';
    const confirm = confirmInput.current?.value ?? '';
    const short = [...password].length < MIN_PASSWORD;
    setSubmitted(true);
    setError('');
    if (short || password !== confirm) {
      setTooShort(short);
      setMismatch(password !== confirm);
      setPendingFocus(short ? 'setup-password' : 'setup-confirm');
      return;
    }
    pendingRef.current = true;
    setPending(true);
    let created = false;
    try {
      await api.setup(username, password, (tokenInput.current?.value ?? '').trim() || undefined);
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
      if (passwordInput.current) passwordInput.current.value = '';
      if (confirmInput.current) confirmInput.current.value = '';
      syncDerived();
      setSubmitted(false);
      if (err instanceof ApiError && err.status === 403) {
        setError('A setup token is required unless you open this page from the machine itself. Enter the token shown in the server log.');
      } else if (err instanceof ApiError && err.status > 0) {
        setError(err.message);
      } else {
        setError('Could not complete setup. Check your connection and try again.');
      }
    } finally {
      pendingRef.current = false;
      setPending(false);
    }
  }

  return (
    <AuthCard verb="Set up" maxWidth={mode === 'admin' ? 400 : 'none'} noValidate onSubmit={mode === 'admin' ? submit : (event) => event.preventDefault()}>
      <Segmented label="Setup choice" options={SETUP_CHOICES} value={mode} onChange={(value) => setMode(value as 'admin' | 'restore')} />
      {mode === 'restore' ? (
        <RestoreFromBackup />
      ) : (
        <>
        <AuthHint>Create the first account.</AuthHint>
        <AuthError message={error} />
        <Field label="Username" id="setup-username">
          {({ readOnly: _, ...c }) => <input {...c} type="text" autoComplete="username" required value={username} onChange={(e) => setUsername(e.target.value)} />}
        </Field>
        <Field label="Password" id="setup-password" hint={`Use at least ${MIN_PASSWORD} characters.`} error={passwordError}>
          {({ readOnly: _, ...c }) => <input {...c} ref={passwordInput} type="password" autoComplete="new-password" required onChange={syncDerived} />}
        </Field>
        <Field label="Confirm password" id="setup-confirm" error={confirmError}>
          {({ readOnly: _, ...c }) => <input {...c} ref={confirmInput} type="password" autoComplete="new-password" required onChange={syncDerived} />}
        </Field>
        <Field label="Setup token (optional)" id="setup-token" hint="Needed unless you open this page from the machine itself.">
          {({ readOnly: _, ...c }) => (
            <input {...c} ref={tokenInput} type="text" autoComplete="off" spellCheck={false} autoCapitalize="off" />
          )}
        </Field>
        <Button variant="primary" type="submit" loading={pending}>
          Create account
        </Button>
        </>
      )}
    </AuthCard>
  );
}
