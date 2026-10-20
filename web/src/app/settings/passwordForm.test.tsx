import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { get } from '$lib/store';
import { afterEach, describe, expect, it } from 'vitest';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type FakeResponse } from '../../vitest-setup';
import { PasswordForm } from './PasswordForm';

let fake: FakeFetch;
afterEach(() => {
  cleanup();
  fake?.restore();
  toasts.reset();
});

const current = () => screen.getByLabelText('Current password') as HTMLInputElement;
const next = () => screen.getByLabelText('New password') as HTMLInputElement;
const confirm = () => screen.getByLabelText('Confirm new password') as HTMLInputElement;
const submit = () => screen.getByRole('button', { name: 'Change password' });
const isDisabled = (el: HTMLElement) => el.hasAttribute('disabled') || el.getAttribute('aria-disabled') === 'true';

async function fill(user: ReturnType<typeof userEvent.setup>, c = 'old-password', n = 'a-new-password-1', k = n) {
  await user.type(current(), c);
  await user.type(next(), n);
  await user.type(confirm(), k);
}

describe('PasswordForm', () => {
  it('uses the right autocomplete hints', () => {
    fake = installFakeFetch({});
    render(<PasswordForm />);
    expect(current().autocomplete).toBe('current-password');
    expect(next().autocomplete).toBe('new-password');
    expect(confirm().autocomplete).toBe('new-password');
    for (const input of [current(), next(), confirm()]) expect(input.type).toBe('password');
  });

  it('keeps submit disabled until all fields are filled and new equals confirm', async () => {
    fake = installFakeFetch({});
    const user = userEvent.setup();
    render(<PasswordForm />);
    expect(isDisabled(submit())).toBe(true);
    await fill(user, 'old-password', 'a-new-password-1', 'a-new-password-2');
    expect(isDisabled(submit())).toBe(true);
    await user.click(submit());
    expect(fake.calls).toHaveLength(0);
  });

  it('blocks a short new password client-side without a request', async () => {
    fake = installFakeFetch({});
    const user = userEvent.setup();
    render(<PasswordForm />);
    await fill(user, 'old-password', 'short');
    await user.click(submit());
    expect(fake.calls).toHaveLength(0);
    expect(document.getElementById('pw-new-error')?.textContent).toContain('Use at least 12 characters.');
    expect(next().getAttribute('aria-invalid')).toBe('true');
    expect(next().value).toBe('short'); // validation errors keep the fields
  });

  it('success clears all fields and toasts without leaking passwords', async () => {
    fake = installFakeFetch({ 'POST /api/password': { status: 204 } });
    const user = userEvent.setup();
    render(<PasswordForm />);
    await fill(user);
    await user.click(submit());
    await waitFor(() => expect(current().value).toBe(''));
    expect(next().value).toBe('');
    expect(confirm().value).toBe('');
    expect(fake.calls[0].body).toEqual({ current: 'old-password', new: 'a-new-password-1' });
    expect(get(toasts).map((t) => t.message)).toEqual(['Password changed. Other sessions were signed out.']);
    expect(document.body.innerHTML).not.toContain('old-password');
    expect(document.body.innerHTML).not.toContain('a-new-password-1');
  });

  it('403 puts the error on the current field and clears the password inputs', async () => {
    fake = installFakeFetch({ 'POST /api/password': { status: 403, body: { error: 'forbidden' } } });
    const user = userEvent.setup();
    render(<PasswordForm />);
    await fill(user);
    await user.click(submit());
    await waitFor(() => expect(screen.getByText('Current password is incorrect.')).toBeTruthy());
    expect(current().getAttribute('aria-invalid')).toBe('true');
    expect(current().value).toBe('');
    expect(next().value).toBe('');
    expect(confirm().value).toBe('');
    await waitFor(() => expect(document.activeElement).toBe(current()));
    expect(get(toasts)).toHaveLength(0);
  });

  it('400 shows the server message on the new field and clears the inputs', async () => {
    fake = installFakeFetch({ 'POST /api/password': { status: 400, body: { error: 'password too weak' } } });
    const user = userEvent.setup();
    render(<PasswordForm />);
    await fill(user);
    await user.click(submit());
    await waitFor(() => expect(screen.getByText('password too weak')).toBeTruthy());
    expect(next().getAttribute('aria-invalid')).toBe('true');
    expect(next().value).toBe('');
    expect(confirm().value).toBe('');
    expect(current().value).toBe('old-password'); // kept so the user only retypes the new one
  });

  it('429 shows a banner and disables the form', async () => {
    fake = installFakeFetch({ 'POST /api/password': { status: 429, body: { error: 'too many attempts' } } });
    const user = userEvent.setup();
    render(<PasswordForm />);
    await fill(user);
    await user.click(submit());
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain('too many attempts');
    expect(alert.textContent).toContain('15 minutes');
    expect(current().disabled).toBe(true);
    expect(next().disabled).toBe(true);
    expect(current().value).toBe('');
    await user.click(screen.getByRole('button', { name: 'Try again' }));
    expect(current().disabled).toBe(false);
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('counts characters, not UTF-16 units: 6 emoji are too short', async () => {
    fake = installFakeFetch({});
    const user = userEvent.setup();
    render(<PasswordForm />);
    await user.type(current(), 'old-password');
    await user.click(next());
    await user.paste('😀'.repeat(6));
    await user.click(confirm());
    await user.paste('😀'.repeat(6));
    await user.click(submit());
    expect(fake.calls).toHaveLength(0);
    expect(document.getElementById('pw-new-error')?.textContent).toContain('Use at least 12 characters.');
  });

  it('a double click sends one request', async () => {
    let release!: (r: FakeResponse) => void;
    fake = installFakeFetch({ 'POST /api/password': () => new Promise<FakeResponse>((resolve) => { release = resolve; }) });
    const user = userEvent.setup();
    render(<PasswordForm />);
    await fill(user);
    const button = submit(); // the same mounted button, clicked twice before React re-renders
    act(() => {
      fireEvent.click(button);
      fireEvent.click(button);
    });
    expect(fake.calls.filter((c) => c.method === 'POST')).toHaveLength(1);
    await act(async () => release({ status: 204 }));
    await waitFor(() => expect(current().value).toBe(''));
    expect(fake.calls.filter((c) => c.method === 'POST')).toHaveLength(1);
  });
});
