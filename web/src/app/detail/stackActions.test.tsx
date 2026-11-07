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

const openMore = async (user: ReturnType<typeof userEvent.setup>) => {
  await user.click(screen.getByRole('button', { name: 'More actions' }));
  return screen.getByRole('group', { name: 'More actions' });
};

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
    await user.click(screen.getByRole('button', { name: 'Pull' }));
    expect(onAct.mock.calls.map((c) => c[0])).toEqual(['restart', 'up', 'pull']);
  });

  it('Down and Remove live in the More menu', async () => {
    const { onAct, user } = setup({ onViewCompose: vi.fn() });
    expect(screen.queryByRole('button', { name: 'Down' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Remove' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Build' })).toBeNull();
    const menu = await openMore(user);
    const items = within(menu).getAllByRole('button').map((i) => i.textContent);
    expect(items).toEqual(['Build images', 'View compose file', 'Copy path', 'Down', 'Remove stack']);
    await user.click(within(menu).getByRole('button', { name: 'Down' }));
    expect(onAct).toHaveBeenCalledWith('down');
    expect(screen.queryByRole('group', { name: 'More actions' })).toBeNull();
  });

  it('Build images and View compose file act from the menu', async () => {
    const onViewCompose = vi.fn();
    const { onAct, user } = setup({ onViewCompose });
    await user.click(within(await openMore(user)).getByRole('button', { name: 'Build images' }));
    expect(onAct).toHaveBeenCalledWith('build');
    await user.click(within(await openMore(user)).getByRole('button', { name: 'View compose file' }));
    expect(onViewCompose).toHaveBeenCalledTimes(1);
  });

  it('More actions is a plain disclosure: expanded state, no menu roles, ordinary buttons in Tab order', async () => {
    const { user } = setup();
    const toggle = screen.getByRole('button', { name: 'More actions' });
    expect(toggle.getAttribute('aria-expanded')).toBe('false');
    expect(toggle.getAttribute('aria-haspopup')).toBeNull();
    await openMore(user);
    expect(toggle.getAttribute('aria-expanded')).toBe('true');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(screen.queryAllByRole('menuitem')).toHaveLength(0);
    await user.tab();
    expect(document.activeElement?.textContent).toBe('Build images');
    await user.tab();
    expect(document.activeElement?.textContent).toBe('Copy path');
  });

  it('a press outside closes the disclosure', async () => {
    const { user } = setup();
    await openMore(user);
    await user.click(document.body);
    expect(screen.queryByRole('group', { name: 'More actions' })).toBeNull();
  });

  it('Escape closes the menu and returns focus to its button', async () => {
    const { user } = setup();
    await openMore(user);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('group', { name: 'More actions' })).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'More actions' }));
  });

  it('disables lifecycle items with the reason and ignores clicks', async () => {
    const { onAct, user } = setup({ lifecycleDisabledReason: 'docker compose is not installed' });
    for (const name of ['Up', 'Restart', 'Pull']) {
      const b = screen.getByRole('button', { name });
      expect(b.getAttribute('aria-disabled')).toBe('true');
      expect(b.getAttribute('title')).toBe('docker compose is not installed');
    }
    const menu = await openMore(user);
    for (const name of ['Build images', 'Down']) {
      expect(within(menu).getByRole('button', { name }).getAttribute('aria-disabled')).toBe('true');
      expect(within(menu).getByRole('button', { name }).getAttribute('title')).toBe('docker compose is not installed');
    }
    await user.click(within(menu).getByRole('button', { name: 'Down' }));
    await user.click(screen.getByRole('button', { name: 'Up' }));
    expect(onAct).not.toHaveBeenCalled();
  });

  it('disables with "an operation is already running" when busy', async () => {
    const { user } = setup({ busy: true });
    expect(screen.getByRole('button', { name: 'Pull' }).getAttribute('title')).toBe('an operation is already running');
    expect(within(await openMore(user)).getByRole('button', { name: 'Remove stack' }).getAttribute('aria-disabled')).toBe('true');
  });

  it('Remove is not disabled by a lifecycle reason alone', async () => {
    const { user } = setup({ lifecycleDisabledReason: 'docker compose is not installed' });
    expect(within(await openMore(user)).getByRole('button', { name: 'Remove stack' }).getAttribute('aria-disabled')).toBeNull();
  });

  const openRemove = async (user: ReturnType<typeof userEvent.setup>) => {
    await user.click(within(await openMore(user)).getByRole('button', { name: 'Remove stack' }));
    return screen.getByRole('alertdialog');
  };

  it('Remove asks first, notes files are kept, and keeps volumes by default without typing', async () => {
    const { onRemove, user } = setup();
    const dialog = await openRemove(user);
    expect(dialog.textContent).toMatch(/files on disk are never deleted/i);
    expect(within(dialog).queryByRole('textbox')).toBeNull();
    expect(onRemove).not.toHaveBeenCalled();
    await user.click(within(dialog).getByRole('button', { name: 'Remove' }));
    expect(onRemove).toHaveBeenCalledWith(false);
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('Remove with volumes requires typing the stack name', async () => {
    const { onRemove, user } = setup();
    const dialog = await openRemove(user);
    await user.click(within(dialog).getByRole('checkbox', { name: /also remove volumes/i }));
    expect(dialog.textContent).toMatch(/volumes cannot be recovered/i);
    const confirm = within(dialog).getByRole('button', { name: 'Remove' });
    expect(confirm.getAttribute('aria-disabled')).toBe('true');
    await user.click(confirm);
    expect(onRemove).not.toHaveBeenCalled();
    await user.type(within(dialog).getByRole('textbox', { name: 'Type web to confirm' }), 'web');
    await user.click(within(dialog).getByRole('button', { name: 'Remove' }));
    expect(onRemove).toHaveBeenCalledWith(true);
  });

  it('Cancel does nothing and the volumes checkbox resets on reopen', async () => {
    const { onRemove, user } = setup();
    let dialog = await openRemove(user);
    await user.click(within(dialog).getByRole('checkbox', { name: /also remove volumes/i }));
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(onRemove).not.toHaveBeenCalled();
    dialog = await openRemove(user);
    expect((within(dialog).getByRole('checkbox', { name: /also remove volumes/i }) as HTMLInputElement).checked).toBe(false);
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
