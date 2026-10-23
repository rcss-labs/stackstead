import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { StackSummary } from '$lib/api/types';
import { StackActions } from './StackActions';

const stack: StackSummary = {
  name: 'web',
  path: '/srv/web',
  project: 'web',
  origin: 'managed',
  status: 'running',
  serviceCount: 2,
  running: 2,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports: [],
  libraryUpdates: 0,
};

afterEach(cleanup);

const setup = (props: Partial<Parameters<typeof StackActions>[0]> = {}) => {
  const onAct = vi.fn();
  const onRemove = vi.fn();
  render(<StackActions stack={stack} onAct={onAct} onRemove={onRemove} {...props} />);
  return { onAct, onRemove, user: userEvent.setup() };
};

describe('StackActions', () => {
  it('fires onAct with the action name', async () => {
    const { onAct, user } = setup();
    expect(screen.getByRole('group', { name: 'Actions for web' })).toBeTruthy();
    await user.click(screen.getByRole('button', { name: 'Restart' }));
    await user.click(screen.getByRole('button', { name: 'Up' }));
    expect(onAct.mock.calls.map((c) => c[0])).toEqual(['restart', 'up']);
  });

  it('disables lifecycle buttons with the reason and ignores clicks', async () => {
    const { onAct, user } = setup({ lifecycleDisabledReason: 'docker compose is not installed' });
    for (const name of ['Up', 'Down', 'Restart', 'Pull', 'Build']) {
      const b = screen.getByRole('button', { name });
      expect(b.getAttribute('aria-disabled')).toBe('true');
      expect(b.getAttribute('title')).toBe('docker compose is not installed');
    }
    await user.click(screen.getByRole('button', { name: 'Up' }));
    expect(onAct).not.toHaveBeenCalled();
  });

  it('disables with "an operation is already running" when busy', () => {
    setup({ busy: true });
    expect(screen.getByRole('button', { name: 'Pull' }).getAttribute('title')).toBe('an operation is already running');
  });

  it('Remove asks first, notes files are kept, and passes the volumes choice', async () => {
    const { onRemove, user } = setup();
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    const dialog = screen.getByRole('alertdialog');
    expect(dialog.textContent).toMatch(/files on disk are never deleted/i);
    expect(onRemove).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole('checkbox', { name: /also remove volumes/i }));
    await user.click(within(dialog).getByRole('button', { name: 'Remove' }));
    expect(onRemove).toHaveBeenCalledWith(true);
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('Remove defaults to keeping volumes, and Cancel does nothing', async () => {
    const { onRemove, user } = setup();
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Cancel' }));
    expect(onRemove).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Remove' }));
    expect(onRemove).toHaveBeenCalledWith(false);
  });

  it('Remove is soft-disabled while busy', () => {
    setup({ busy: true });
    expect(screen.getByRole('button', { name: 'Remove' }).getAttribute('aria-disabled')).toBe('true');
  });

  it('Remove is not disabled by a lifecycle reason alone', () => {
    setup({ lifecycleDisabledReason: 'docker compose is not installed' });
    expect(screen.getByRole('button', { name: 'Remove' }).getAttribute('aria-disabled')).toBeNull();
  });

  it('the volumes checkbox resets after cancel and reopen', async () => {
    const { user } = setup();
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    await user.click(screen.getByRole('checkbox', { name: /also remove volumes/i }));
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Cancel' }));
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    expect((screen.getByRole('checkbox', { name: /also remove volumes/i }) as HTMLInputElement).checked).toBe(false);
  });

  it('Up is the primary button with the play icon when the stack is not running', () => {
    setup({ stack: { ...stack, status: 'stopped', running: 0 } });
    const up = screen.getByRole('button', { name: 'Up' });
    expect(up.getAttribute('data-variant')).toBe('primary');
    expect(up.querySelector('svg')).not.toBeNull();
    expect(screen.getByRole('button', { name: 'Restart' }).getAttribute('data-variant')).toBe('secondary');
  });

  it('Up is secondary while the stack is running', () => {
    setup();
    expect(screen.getByRole('button', { name: 'Up' }).getAttribute('data-variant')).toBe('secondary');
  });
});
