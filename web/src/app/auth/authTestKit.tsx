// Shared mount and helpers for the Login and Setup screen tests.
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { bootstrapSession } from '$lib/api/session';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { LoginScreen } from './LoginScreen';
import { SetupScreen } from './SetupScreen';

function Probe() {
  const here = useLocation();
  return <output data-testid="location">{here.pathname + here.search}</output>;
}

/** Login at /login, Setup at /setup, anything else is "elsewhere"; the probe shows the router location. */
export function mountAuth(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/login" element={<LoginScreen />} />
        <Route path="/setup" element={<SetupScreen />} />
        <Route path="*" element={<p>elsewhere</p>} />
      </Routes>
      <Probe />
    </MemoryRouter>,
  );
}

export const location = () => screen.getByTestId('location').textContent;

export async function fill(user: string, pass: string) {
  const u = userEvent.setup();
  await u.type(screen.getByLabelText('Username'), user);
  await u.type(screen.getByLabelText('Password'), pass);
  return u;
}

export const PW = 'correct-horse-battery';

export async function fillForm(opts: { confirm?: string; token?: string; pw?: string } = {}) {
  const u = userEvent.setup();
  await u.type(screen.getByLabelText('Username'), 'admin');
  await u.type(screen.getByLabelText('Password'), opts.pw ?? PW);
  await u.type(screen.getByLabelText('Confirm password'), opts.confirm ?? PW);
  if (opts.token) await u.type(screen.getByLabelText(/Setup token/), opts.token);
  return u;
}

export const submit = (u: ReturnType<typeof userEvent.setup>) => u.click(screen.getByRole('button', { name: 'Create account' }));

/** Installs the first-run fetch routes and boots the session into `needs-setup`. */
export async function needsSetup(): Promise<FakeFetch> {
  const fake = installFakeFetch({
    'GET /api/me': { status: 401, body: { error: 'x' } },
    'GET /api/setup/status': { body: { needsSetup: true } },
    'POST /api/setup': { body: {} },
    'POST /api/login': { body: {} },
  });
  await act(async () => {
    await bootstrapSession();
  });
  return fake;
}
