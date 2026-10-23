import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { installFakeFetch, type FakeFetch, type Routes } from '../../vitest-setup';
import { GitHubCard } from './GitHubCard';

let fake: FakeFetch;
afterEach(() => fake.restore());

function mount(settings: { configured: boolean; login: string }, routes: Routes = {}) {
  fake = installFakeFetch({ 'GET /api/settings/github': { body: settings }, ...routes });
  return render(<GitHubCard />);
}
const calls = (method: string, path: string) => fake.calls.filter((c) => c.method === method && c.path === path);

describe('GitHubCard', () => {
  it('says Not configured without a token', async () => {
    mount({ configured: false, login: '' });
    expect(await screen.findByText('Not configured')).toBeTruthy();
  });

  it('shows the signed-in login when configured, with an empty masked token field', async () => {
    mount({ configured: true, login: 'octocat' });
    expect(await screen.findByText('Signed in as octocat')).toBeTruthy();
    const token = screen.getByLabelText('Token') as HTMLInputElement;
    expect(token.type).toBe('password');
    expect(token.value).toBe('');
  });

  it('saves only the token and then shows the new login', async () => {
    mount({ configured: false, login: '' }, { 'PUT /api/settings/github': { body: { configured: true, login: 'octocat' } } });
    await userEvent.type(await screen.findByLabelText('Token'), 'ghp_secret');
    await userEvent.click(screen.getByRole('button', { name: 'Save token' }));
    await waitFor(() => expect(calls('PUT', '/api/settings/github')).toHaveLength(1));
    expect(calls('PUT', '/api/settings/github')[0].body).toEqual({ token: 'ghp_secret' });
    expect(await screen.findByText('Signed in as octocat')).toBeTruthy();
    expect((screen.getByLabelText('Token') as HTMLInputElement).value).toBe('');
  });

  it('keeps Save disabled for a whitespace-only token and trims what it sends', async () => {
    mount({ configured: false, login: '' }, { 'PUT /api/settings/github': { body: { configured: true, login: 'o' } } });
    const field = await screen.findByLabelText('Token');
    await userEvent.type(field, '   ');
    await userEvent.click(screen.getByRole('button', { name: 'Save token' }));
    expect(calls('PUT', '/api/settings/github')).toHaveLength(0);
    await userEvent.type(field, 'tok ');
    await userEvent.click(screen.getByRole('button', { name: 'Save token' }));
    await waitFor(() => expect(calls('PUT', '/api/settings/github')).toHaveLength(1));
    expect(calls('PUT', '/api/settings/github')[0].body).toEqual({ token: 'tok' });
  });

  it('shows the test message inline', async () => {
    mount({ configured: true, login: 'octocat' }, { 'POST /api/settings/github/test': { body: { ok: false, login: '', message: 'Bad credentials' } } });
    await userEvent.click(await screen.findByRole('button', { name: 'Test connection' }));
    expect(await screen.findByText(/Bad credentials/)).toBeTruthy();
  });
});
