import { useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import { useNavigate, useSearchParams } from 'react-router';
import { ApiError } from '$lib/api/client';
import { login } from '$lib/api/session';
import { safeNext } from '$lib/router';
import { Button } from '../components/Button';
import { Field } from '../components/Field';

/** Minimal working sign-in until Plan 22 brings the designed screen; App owns the main landmark. */
export default function Login() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [pending, setPending] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    document.title = 'Sign in · Stackstead';
  }, []);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pending) return;
    setPending(true);
    setError('');
    try {
      await login(username, password);
      navigate(safeNext(searchParams.get('next')), { replace: true });
    } catch (err) {
      setPassword('');
      if (err instanceof ApiError && err.status === 401) setError('invalid username or password');
      else if (err instanceof ApiError && err.status === 429) setError(err.message);
      else setError('Could not sign in. Check your connection and try again.');
    } finally {
      setPending(false);
    }
  }

  return (
    <form onSubmit={submit}>
      <h1>Sign in to Stackstead</h1>
      {error && <p role="alert">{error}</p>}
      <Field label="Username" id="login-username">
        {(c) => <input {...c} type="text" autoComplete="username" required value={username} onChange={(e) => setUsername(e.target.value)} />}
      </Field>
      <Field label="Password" id="login-password">
        {(c) => (
          <input {...c} type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} />
        )}
      </Field>
      <Button variant="primary" type="submit" loading={pending}>
        Log in
      </Button>
    </form>
  );
}
