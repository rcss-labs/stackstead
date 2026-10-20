import { useEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { useNavigate, useSearchParams } from 'react-router';
import { ApiError } from '$lib/api/client';
import { login } from '$lib/api/session';
import { safeNext } from '$lib/router';
import { Button } from '../components/Button';
import { Field } from '../components/Field';
import { AuthCard, AuthError } from './AuthCard';

// The password input is uncontrolled: its value lives only in the DOM input, is read through a ref at
// submit, and is never kept in state, a value attribute, a message, storage or the URL.
export function LoginScreen() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const [username, setUsername] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState('');
  const pendingRef = useRef(false);
  const passwordInput = useRef<HTMLInputElement>(null);

  useEffect(() => {
    document.title = 'Sign in · Stackstead';
  }, []);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pendingRef.current) return;
    pendingRef.current = true;
    setPending(true);
    setError('');
    try {
      await login(username, passwordInput.current?.value ?? '');
      navigate(safeNext(searchParams.get('next')), { replace: true });
    } catch (err) {
      if (passwordInput.current) passwordInput.current.value = '';
      if (err instanceof ApiError && err.status === 401) setError('invalid username or password');
      else if (err instanceof ApiError && err.status === 429) setError(err.message);
      else setError('Could not sign in. Check your connection and try again.');
    } finally {
      pendingRef.current = false;
      setPending(false);
    }
  }

  return (
    <AuthCard verb="Sign in to" maxWidth={360} onSubmit={submit}>
      <AuthError message={error} />
      <Field label="Username" id="login-username">
        {({ readOnly: _, ...c }) => <input {...c} type="text" autoComplete="username" required value={username} onChange={(e) => setUsername(e.target.value)} />}
      </Field>
      <Field label="Password" id="login-password">
        {({ readOnly: _, ...c }) => <input {...c} ref={passwordInput} type="password" autoComplete="current-password" required />}
      </Field>
      <Button variant="primary" type="submit" loading={pending}>
        Sign in
      </Button>
    </AuthCard>
  );
}
