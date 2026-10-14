import { render, screen, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { StackSummary } from '$lib/api/types';
import ActionsBar from './ActionsBar.svelte';

const stack: StackSummary = {
  name: 'web',
  path: '/srv/web',
  project: 'web',
  origin: 'created',
  status: 'running',
  serviceCount: 2,
  running: 2,
} as StackSummary;

const setup = (props: Record<string, unknown> = {}) => {
  const onact = vi.fn();
  const onremove = vi.fn();
  render(ActionsBar, { stack, onact, onremove, ...props });
  return { onact, onremove, user: userEvent.setup() };
};

describe('ActionsBar', () => {
  it('fires onact with the action name', async () => {
    const { onact, user } = setup();
    await user.click(screen.getByRole('button', { name: 'Restart' }));
    await user.click(screen.getByRole('button', { name: 'Up' }));
    expect(onact.mock.calls.map((c) => c[0])).toEqual(['restart', 'up']);
  });

  it('disables lifecycle buttons with the reason and ignores clicks', async () => {
    const { onact, user } = setup({ lifecycleDisabledReason: 'docker compose is not installed' });
    for (const name of ['Up', 'Down', 'Restart', 'Pull', 'Build']) {
      const b = screen.getByRole('button', { name });
      expect(b.getAttribute('aria-disabled')).toBe('true');
      expect(b.getAttribute('title')).toBe('docker compose is not installed');
    }
    await user.click(screen.getByRole('button', { name: 'Up' }));
    expect(onact).not.toHaveBeenCalled();
  });

  it('disables with "an operation is already running" when busy', () => {
    setup({ busy: true });
    expect(screen.getByRole('button', { name: 'Pull' }).getAttribute('title')).toBe('an operation is already running');
  });

  it('Remove asks first, notes files are kept, and passes the volumes choice', async () => {
    const { onremove, user } = setup();
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    const dialog = screen.getByRole('alertdialog');
    expect(dialog.textContent).toMatch(/files on disk are never deleted/i);
    expect(onremove).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole('checkbox', { name: /also remove volumes/i }));
    await user.click(within(dialog).getByRole('button', { name: 'Remove' }));
    expect(onremove).toHaveBeenCalledWith(true);
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('Remove defaults to keeping volumes, and Cancel does nothing', async () => {
    const { onremove, user } = setup();
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Cancel' }));
    expect(onremove).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Remove' }));
    expect(onremove).toHaveBeenCalledWith(false);
  });
});
