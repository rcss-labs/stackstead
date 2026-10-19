import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { ConfirmDialog } from './ConfirmDialog';
import { Dialog } from './Dialog';

const body = (
  <div>
    <button>First</button>
    <button>Last</button>
  </div>
);

describe('Dialog', () => {
  it('is absent when closed and modal with a title when open', () => {
    const { unmount } = render(
      <Dialog open={false} title="T" onClose={() => {}}>
        {body}
      </Dialog>,
    );
    expect(screen.queryByRole('dialog')).toBeNull();
    unmount();
    render(
      <Dialog open title="Delete it" onClose={() => {}}>
        {body}
      </Dialog>,
    );
    const dlg = screen.getByRole('dialog', { name: 'Delete it' });
    expect(dlg.getAttribute('aria-modal')).toBe('true');
    expect(dlg.parentElement?.parentElement).toBe(document.body);
  });

  it('moves focus in, traps Tab, closes on Escape and restores focus to the opener', async () => {
    const user = userEvent.setup();
    const opener = document.createElement('button');
    opener.textContent = 'Open';
    document.body.append(opener);
    opener.focus();
    const onClose = vi.fn();
    const { rerender } = render(
      <Dialog open={false} title="T" onClose={onClose}>
        {body}
      </Dialog>,
    );
    rerender(
      <Dialog open title="T" onClose={onClose}>
        {body}
      </Dialog>,
    );
    const dlg = screen.getByRole('dialog');
    await waitFor(() => expect(dlg.contains(document.activeElement)).toBe(true));
    const first = screen.getByRole('button', { name: 'First' });
    const last = screen.getByRole('button', { name: 'Last' });
    last.focus();
    await user.tab();
    expect(document.activeElement).toBe(first);
    await user.tab({ shift: true });
    expect(document.activeElement).toBe(last);
    await user.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
    rerender(
      <Dialog open={false} title="T" onClose={onClose}>
        {body}
      </Dialog>,
    );
    await waitFor(() => expect(document.activeElement).toBe(opener));
    opener.remove();
  });

  it('restores focus to a clicked opener that never took focus (Safari and Firefox on macOS)', async () => {
    const user = userEvent.setup();
    const opener = document.createElement('button');
    opener.textContent = 'Open';
    document.body.append(opener);
    (document.activeElement as HTMLElement | null)?.blur();
    fireEvent.click(opener);
    expect(document.activeElement).toBe(document.body);
    const { rerender } = render(
      <Dialog open={false} title="T" onClose={() => {}}>
        {body}
      </Dialog>,
    );
    rerender(
      <Dialog open title="T" onClose={() => {}}>
        {body}
      </Dialog>,
    );
    const dlg = screen.getByRole('dialog');
    await waitFor(() => expect(dlg.contains(document.activeElement)).toBe(true));
    await user.keyboard('{Escape}');
    rerender(
      <Dialog open={false} title="T" onClose={() => {}}>
        {body}
      </Dialog>,
    );
    await waitFor(() => expect(document.activeElement).toBe(opener));
    opener.remove();
  });

  it('closes on Escape even when focus is outside the dialog', async () => {
    const onClose = vi.fn();
    render(
      <Dialog open title="T" onClose={onClose}>
        {body}
      </Dialog>,
    );
    (document.activeElement as HTMLElement | null)?.blur();
    await userEvent.setup().keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('closes only the topmost of nested dialogs per Escape, and ignores handled keys', async () => {
    const user = userEvent.setup();
    const outer = vi.fn();
    const inner = vi.fn();
    render(
      <Dialog open title="Outer" onClose={outer}>
        {body}
      </Dialog>,
    );
    const innerView = render(
      <Dialog open title="Inner" onClose={inner}>
        {body}
      </Dialog>,
    );
    await user.keyboard('{Escape}');
    expect(inner).toHaveBeenCalledTimes(1);
    expect(outer).not.toHaveBeenCalled();
    innerView.unmount();
    const handled = (event: KeyboardEvent) => event.preventDefault();
    window.addEventListener('keydown', handled, { capture: true, once: true });
    await user.keyboard('{Escape}');
    expect(outer).not.toHaveBeenCalled(); // already handled by someone else
    await user.keyboard('{Escape}');
    expect(outer).toHaveBeenCalledTimes(1);
  });

  it('does not focus hidden controls', async () => {
    render(
      <Dialog open title="T" onClose={() => {}}>
        <div>
          <button hidden>Ghost</button>
          <button>Real</button>
        </div>
      </Dialog>,
    );
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Real' })));
  });

  it('marks the app root inert while open and clears it on close', async () => {
    const root = document.createElement('div');
    root.id = 'app';
    document.body.append(root);
    const { rerender } = render(
      <Dialog open title="T" onClose={() => {}}>
        {body}
      </Dialog>,
    );
    await waitFor(() => expect(root.inert).toBe(true));
    rerender(
      <Dialog open={false} title="T" onClose={() => {}}>
        {body}
      </Dialog>,
    );
    await waitFor(() => expect(root.inert).toBe(false));
    root.remove();
  });
});

describe('ConfirmDialog', () => {
  it('shows the message and reports confirm and cancel', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <ConfirmDialog
        open
        title="Remove stack"
        message="This stops all services."
        confirmLabel="Remove"
        danger
        onConfirm={onConfirm}
        onCancel={onCancel}
      />,
    );
    const dlg = screen.getByRole('alertdialog', { name: 'Remove stack' });
    expect(dlg.textContent).toContain('This stops all services.');
    expect(document.getElementById(dlg.getAttribute('aria-describedby')!)?.textContent).toBe('This stops all services.');
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    expect(onConfirm).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it('renders extra content between the message and the actions', () => {
    render(
      <ConfirmDialog open title="Remove" message="m" confirmLabel="Remove" onConfirm={() => {}} onCancel={() => {}}>
        <label>
          <input type="checkbox" /> Also remove volumes
        </label>
      </ConfirmDialog>,
    );
    expect(screen.getByRole('checkbox', { name: 'Also remove volumes' })).toBeTruthy();
  });

  it('cancels on Escape and starts focus on Cancel, not the destructive action', async () => {
    const onCancel = vi.fn();
    render(<ConfirmDialog open title="Remove" message="m" confirmLabel="Remove" danger onConfirm={() => {}} onCancel={onCancel} />);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Cancel' })));
    await userEvent.setup().keyboard('{Escape}');
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it('forwards pending and a soft-disabled reason to confirm', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const { rerender } = render(
      <ConfirmDialog open title="Remove" message="m" confirmLabel="Remove" confirmDisabledReason="Type the name first" onConfirm={onConfirm} onCancel={() => {}} />,
    );
    const confirm = screen.getByRole('button', { name: 'Remove' });
    expect(confirm.getAttribute('aria-disabled')).toBe('true');
    expect(confirm.getAttribute('aria-describedby')).toBeTruthy();
    await user.click(confirm);
    expect(onConfirm).not.toHaveBeenCalled();
    rerender(<ConfirmDialog open title="Remove" message="m" confirmLabel="Remove" pending onConfirm={onConfirm} onCancel={() => {}} />);
    expect(screen.getByRole('button', { name: 'Remove' }).getAttribute('aria-busy')).toBe('true');
  });
});
