import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { Dialog } from '../components/Dialog';
import { toasts } from '$lib/stores/toasts';
import { Toasts } from './Toasts';

function mount(extra?: React.ReactNode) {
  return render(
    <MemoryRouter>
      <div id="app">{extra}</div>
      <Toasts />
    </MemoryRouter>,
  );
}

describe('Toasts', () => {
  beforeEach(() => toasts.reset());
  afterEach(() => toasts.reset());

  it('renders pushed toasts in a polite live region and dismisses on click', async () => {
    const user = userEvent.setup();
    mount();
    const region = screen.getByRole('region', { name: 'Notifications' });
    expect(region.getAttribute('aria-live')).toBe('polite');
    act(() => void toasts.push({ kind: 'success', message: '<b>Saved</b>', timeoutMs: 0 }));
    expect(region.textContent).toContain('<b>Saved</b>');
    expect(region.querySelector('b')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Dismiss notification' }));
    expect(region.textContent).not.toContain('Saved');
  });

  it('uses role=alert for danger toasts only', () => {
    mount();
    act(() => {
      toasts.push({ kind: 'info', message: 'fyi', timeoutMs: 0 });
      toasts.push({ kind: 'warning', message: 'hmm', timeoutMs: 0 });
    });
    expect(screen.queryByRole('alert')).toBeNull();
    act(() => void toasts.push({ kind: 'danger', message: 'boom' }));
    expect(screen.getByRole('alert').textContent).toContain('boom');
    expect(screen.getAllByRole('alert')).toHaveLength(1);
  });

  it('lives outside #app so a modal dialog does not make it inert, and stays dismissable', async () => {
    const user = userEvent.setup();
    mount(
      <Dialog open title="Modal" onClose={() => {}}>
        <button>Inside</button>
      </Dialog>,
    );
    act(() => void toasts.push({ kind: 'danger', message: 'Still here' }));
    const app = document.getElementById('app')!;
    expect(app.inert).toBe(true);
    const region = screen.getByRole('region', { name: 'Notifications', hidden: true });
    expect(app.contains(region)).toBe(false);
    expect(region.parentElement).toBe(document.body);
    expect(region.closest('[inert]')).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Dismiss notification' }));
    expect(region.textContent).not.toContain('Still here');
  });

  it('pending toast shows a spinner and its View link points at the operation', () => {
    mount();
    act(
      () =>
        void toasts.push({
          kind: 'info',
          message: 'Starting web',
          pending: true,
          link: { label: 'View', href: '/stacks/web?tab=operations&op=op1' },
        }),
    );
    expect(screen.getByRole('status')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'View' }).getAttribute('href')).toBe('/stacks/web?tab=operations&op=op1');
  });
});
