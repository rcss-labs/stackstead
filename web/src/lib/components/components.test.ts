import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { createRawSnippet } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { toasts } from '../stores/toasts';
import Banner from './Banner.svelte';
import Button from './Button.svelte';
import ConfirmDialog from './ConfirmDialog.svelte';
import CopyButton from './CopyButton.svelte';
import Dialog from './Dialog.svelte';
import EmptyState from './EmptyState.svelte';
import Field from './Field.svelte';
import ListEditor from './ListEditor.svelte';
import Nav from './Nav.svelte';
import RelativeTime from './RelativeTime.svelte';
import Spinner from './Spinner.svelte';
import StatusBadge from './StatusBadge.svelte';
import Tabs from './Tabs.svelte';
import Toasts from './Toasts.svelte';

const html = (markup: string) => createRawSnippet(() => ({ render: () => markup }));

afterEach(() => toasts.reset());

describe('Banner', () => {
  it('uses role=alert for danger and role=status otherwise, with a title', () => {
    const { unmount } = render(Banner, { kind: 'danger', title: 'Docker is down', children: html('<span>detail</span>') });
    expect(screen.getByRole('alert').textContent).toContain('Docker is down');
    unmount();
    render(Banner, { kind: 'info', title: 'FYI', children: html('<span>x</span>') });
    expect(screen.getByRole('status').textContent).toContain('FYI');
  });

  it('can be dismissed', async () => {
    render(Banner, { kind: 'warning', title: 'Heads up', dismissible: true, children: html('<span>x</span>') });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Dismiss' }));
    expect(screen.queryByText('Heads up')).toBeNull();
  });
});

describe('Toasts', () => {
  it('renders pushed toasts in a polite live region and dismisses on click', async () => {
    render(Toasts);
    const region = screen.getByRole('region', { name: 'Notifications' });
    expect(region.getAttribute('aria-live')).toBe('polite');
    toasts.push({ kind: 'success', message: 'Saved <b>ok</b>', timeoutMs: 0 });
    await waitFor(() => expect(region.textContent).toContain('Saved <b>ok</b>'));
    expect(region.querySelector('b')).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: /dismiss/i }));
    await waitFor(() => expect(region.textContent).not.toContain('Saved'));
  });
});

describe('Dialog', () => {
  const body = html('<div><button>First</button><button>Last</button></div>');

  it('is absent when closed and modal with a title when open', () => {
    const { unmount } = render(Dialog, { open: false, title: 'T', onclose: () => {}, children: body });
    expect(screen.queryByRole('dialog')).toBeNull();
    unmount();
    render(Dialog, { open: true, title: 'Delete it', onclose: () => {}, children: body });
    const dlg = screen.getByRole('dialog', { name: 'Delete it' });
    expect(dlg.getAttribute('aria-modal')).toBe('true');
  });

  it('moves focus in, traps Tab, closes on Escape and restores focus to the opener', async () => {
    const user = userEvent.setup();
    const opener = document.createElement('button');
    opener.textContent = 'Open';
    document.body.append(opener);
    opener.focus();
    const onclose = vi.fn();
    const { rerender } = render(Dialog, { open: false, title: 'T', onclose, children: body });
    await rerender({ open: true });
    const dlg = screen.getByRole('dialog');
    await waitFor(() => expect(dlg.contains(document.activeElement)).toBe(true));
    const first = screen.getByRole('button', { name: 'First' });
    const last = screen.getByRole('button', { name: 'Last' });
    last.focus();
    await user.tab();
    expect(dlg.contains(document.activeElement)).toBe(true);
    expect(document.activeElement).not.toBe(last);
    first.focus();
    await user.tab({ shift: true });
    expect(dlg.contains(document.activeElement)).toBe(true);
    expect(document.activeElement).not.toBe(first);
    await user.keyboard('{Escape}');
    expect(onclose).toHaveBeenCalledTimes(1);
    await rerender({ open: false });
    await waitFor(() => expect(document.activeElement).toBe(opener));
    opener.remove();
  });

  it('marks the app root inert while open and clears it on close', async () => {
    const root = document.createElement('div');
    root.id = 'app';
    document.body.append(root);
    const { rerender } = render(Dialog, { open: true, title: 'T', onclose: () => {}, children: body });
    await waitFor(() => expect(root.inert).toBe(true));
    await rerender({ open: false });
    await waitFor(() => expect(root.inert).toBe(false));
    root.remove();
  });
});

describe('ConfirmDialog', () => {
  it('shows the message and reports confirm and cancel', async () => {
    const user = userEvent.setup();
    const onconfirm = vi.fn();
    const oncancel = vi.fn();
    render(ConfirmDialog, {
      open: true,
      title: 'Remove stack',
      message: 'This stops all services.',
      confirmLabel: 'Remove',
      danger: true,
      onconfirm,
      oncancel,
    });
    expect(screen.getByRole('dialog', { name: 'Remove stack' }).textContent).toContain('This stops all services.');
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    expect(onconfirm).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(oncancel).toHaveBeenCalledTimes(1);
  });

  it('cancels on Escape and starts focus on Cancel, not the destructive action', async () => {
    const oncancel = vi.fn();
    render(ConfirmDialog, {
      open: true,
      title: 'Remove',
      message: 'm',
      confirmLabel: 'Remove',
      danger: true,
      onconfirm: () => {},
      oncancel,
    });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Cancel' })));
    await userEvent.setup().keyboard('{Escape}');
    expect(oncancel).toHaveBeenCalledTimes(1);
  });
});

describe('Field', () => {
  const input = html('<input id="name" />');

  it('associates the label with the control', () => {
    render(Field, { label: 'Name', id: 'name', children: input });
    expect(screen.getByLabelText('Name').id).toBe('name');
  });

  it('wires aria-invalid and aria-describedby to the error and hint', async () => {
    const { rerender } = render(Field, { label: 'Name', id: 'name', hint: 'Letters only', children: input });
    const control = screen.getByLabelText('Name');
    await waitFor(() => expect(control.getAttribute('aria-invalid')).toBeNull());
    await rerender({ error: 'Required' });
    await waitFor(() => expect(control.getAttribute('aria-invalid')).toBe('true'));
    const described = control.getAttribute('aria-describedby') ?? '';
    const text = described
      .split(' ')
      .map((id) => document.getElementById(id)?.textContent)
      .join(' ');
    expect(text).toContain('Required');
    expect(text).toContain('Letters only');
  });

  it('marks a read-only control and explains why', async () => {
    render(Field, { label: 'Image', id: 'name', readonly: true, readonlyReason: 'Set by an anchor', children: input });
    const control = screen.getByLabelText('Image') as HTMLInputElement;
    await waitFor(() => expect(control.readOnly).toBe(true));
    expect(control.getAttribute('aria-readonly')).toBe('true');
    const ids = (control.getAttribute('aria-describedby') ?? '').split(' ');
    expect(ids.map((id) => document.getElementById(id)?.textContent).join(' ')).toContain('Set by an anchor');
  });
});

describe('Button', () => {
  it('fires onclick', async () => {
    const onclick = vi.fn();
    render(Button, { variant: 'primary', onclick, children: html('<span>Go</span>') });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Go' }));
    expect(onclick).toHaveBeenCalledTimes(1);
  });

  it('exposes disabledReason to AT, stays focusable and does not fire', async () => {
    const user = userEvent.setup();
    const onclick = vi.fn();
    render(Button, {
      variant: 'primary',
      disabledReason: 'Compose is unavailable',
      onclick,
      children: html('<span>Apply</span>'),
    });
    const btn = screen.getByRole('button', { name: 'Apply' });
    expect(btn.getAttribute('aria-disabled')).toBe('true');
    expect((btn as HTMLButtonElement).disabled).toBe(false);
    expect(btn.getAttribute('title')).toBe('Compose is unavailable');
    expect(screen.getByRole('button', { name: 'Apply' }).getAttribute('aria-describedby')).toBeTruthy();
    const desc = document.getElementById(btn.getAttribute('aria-describedby')!);
    expect(desc?.textContent).toBe('Compose is unavailable');
    await user.tab();
    expect(document.activeElement).toBe(btn);
    await user.click(btn);
    expect(onclick).not.toHaveBeenCalled();
  });

  it('is busy and inert while loading, and truly disabled when disabled', async () => {
    const onclick = vi.fn();
    const { rerender } = render(Button, { variant: 'secondary', loading: true, onclick, children: html('<span>Save</span>') });
    const btn = screen.getByRole('button', { name: /Save/ });
    expect(btn.getAttribute('aria-busy')).toBe('true');
    await userEvent.setup().click(btn);
    expect(onclick).not.toHaveBeenCalled();
    await rerender({ loading: false, disabled: true });
    expect((screen.getByRole('button', { name: 'Save' }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe('StatusBadge', () => {
  it.each([
    ['running', 'running'],
    ['partial', 'partial'],
    ['stopped', 'stopped'],
    ['unavailable', 'unavailable'],
    ['unknown', 'unknown'],
    ['succeeded', 'succeeded'],
    ['failed', 'failed'],
    ['cancelled', 'cancelled'],
    ['timed_out', 'timed out'],
  ] as const)('renders text for %s', (status, text) => {
    render(StatusBadge, { status });
    const badge = screen.getByText(text);
    expect(badge).toBeTruthy();
    // an icon glyph accompanies the text and is hidden from AT
    expect(badge.parentElement?.querySelector('[aria-hidden="true"]')?.textContent?.length).toBeGreaterThan(0);
  });
});

describe('Tabs', () => {
  const tabs = [
    { id: 'a', label: 'Alpha' },
    { id: 'b', label: 'Beta' },
    { id: 'c', label: 'Gamma' },
  ];

  it('exposes tablist/tab/tabpanel with the selected tab', () => {
    render(Tabs, { tabs, selected: 'b', onselect: () => {}, children: html('<p>panel body</p>') });
    expect(screen.getByRole('tablist')).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Beta' }).getAttribute('aria-selected')).toBe('true');
    expect(screen.getByRole('tab', { name: 'Alpha' }).getAttribute('aria-selected')).toBe('false');
    expect(screen.getByRole('tab', { name: 'Alpha' }).getAttribute('tabindex')).toBe('-1');
    expect(screen.getByRole('tabpanel', { name: 'Beta' }).textContent).toContain('panel body');
  });

  it('arrow keys, Home and End move selection and focus, wrapping around', async () => {
    const user = userEvent.setup();
    const onselect = vi.fn();
    render(Tabs, { tabs, selected: 'a', onselect });
    screen.getByRole('tab', { name: 'Alpha' }).focus();
    await user.keyboard('{ArrowRight}');
    expect(onselect).toHaveBeenLastCalledWith('b');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Beta' }));
    await user.keyboard('{ArrowLeft}');
    expect(onselect).toHaveBeenLastCalledWith('a');
    await user.keyboard('{ArrowLeft}');
    expect(onselect).toHaveBeenLastCalledWith('c');
    await user.keyboard('{Home}');
    expect(onselect).toHaveBeenLastCalledWith('a');
    await user.keyboard('{End}');
    expect(onselect).toHaveBeenLastCalledWith('c');
  });

  it('selects on click', async () => {
    const onselect = vi.fn();
    render(Tabs, { tabs, selected: 'a', onselect });
    await userEvent.setup().click(screen.getByRole('tab', { name: 'Gamma' }));
    expect(onselect).toHaveBeenCalledWith('c');
  });
});

describe('ListEditor', () => {
  const row = createRawSnippet<[string, number, (v: string) => void]>((item, index) => ({
    render: () => `<input aria-label="Item ${index() + 1}" value="${item()}" />`,
  }));

  function setup(initial: string[], extra: Record<string, unknown> = {}) {
    const onchange = vi.fn();
    const utils = render(ListEditor<string>, {
      items: initial,
      addLabel: 'Add item',
      newItem: () => '',
      row,
      onchange: (items: string[]) => {
        onchange(items);
        void utils.rerender({ items });
      },
      ...extra,
    });
    return { ...utils, onchange };
  }

  it('adds a row and focuses its first input', async () => {
    const user = userEvent.setup();
    const { onchange } = setup(['a']);
    await user.click(screen.getByRole('button', { name: 'Add item' }));
    expect(onchange).toHaveBeenCalledWith(['a', '']);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Item 2')));
  });

  it('removing a row focuses the previous row; removing the first focuses the next', async () => {
    const user = userEvent.setup();
    const { onchange } = setup(['a', 'b', 'c']);
    await user.click(screen.getByRole('button', { name: 'Remove item 3' }));
    expect(onchange).toHaveBeenLastCalledWith(['a', 'b']);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Item 2')));
    await user.click(screen.getByRole('button', { name: 'Remove item 1' }));
    expect(onchange).toHaveBeenLastCalledWith(['b']);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Item 1')));
  });

  it('removing the last remaining row focuses the add button', async () => {
    const user = userEvent.setup();
    setup(['a']);
    await user.click(screen.getByRole('button', { name: 'Remove item 1' }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add item' })));
  });

  it('shows per-row errors and hides add/remove when read-only', () => {
    const { unmount } = setup(['a', 'b'], { errors: { 1: 'Invalid port' } });
    expect(screen.getByText('Invalid port')).toBeTruthy();
    unmount();
    setup(['a'], { readonly: true });
    expect(screen.queryByRole('button', { name: 'Add item' })).toBeNull();
    expect(screen.queryByRole('button', { name: /Remove/ })).toBeNull();
  });
});

describe('CopyButton', () => {
  it('copies via the clipboard API and announces Copied', async () => {
    const user = userEvent.setup(); // setup() installs its own clipboard stub; override it afterwards
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    render(CopyButton, { text: 'secret-token', label: 'Copy token' });
    await user.click(screen.getByRole('button', { name: 'Copy token' }));
    expect(writeText).toHaveBeenCalledWith('secret-token');
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Copied'));
  });

  it('falls back to a textarea and execCommand without the clipboard API', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    const exec = vi.fn().mockReturnValue(true);
    (document as unknown as { execCommand: unknown }).execCommand = exec;
    render(CopyButton, { text: 'abc' });
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    expect(exec).toHaveBeenCalledWith('copy');
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Copied'));
    expect(document.querySelector('textarea')).toBeNull();
  });
});

describe('Spinner and EmptyState', () => {
  it('Spinner announces its label as status', () => {
    render(Spinner, { label: 'Loading stacks' });
    expect(screen.getByRole('status').textContent).toContain('Loading stacks');
  });

  it('EmptyState shows a title, description and action', () => {
    render(EmptyState, { title: 'No stacks', description: 'Create one.', children: html('<button>Create</button>') });
    expect(screen.getByRole('heading', { name: 'No stacks' })).toBeTruthy();
    expect(screen.getByText('Create one.')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Create' })).toBeTruthy();
  });
});

describe('RelativeTime', () => {
  it('renders relative text with the absolute time as title', () => {
    const iso = new Date(Date.now() - 3 * 60_000).toISOString();
    render(RelativeTime, { iso });
    const el = screen.getByText('3m ago');
    expect(el.tagName).toBe('TIME');
    expect(el.getAttribute('datetime')).toBe(iso);
    expect(el.getAttribute('title')).toBeTruthy();
  });

  it('shows an em dash for an unparseable timestamp', () => {
    render(RelativeTime, { iso: 'nope' });
    expect(screen.getByText('—')).toBeTruthy();
  });
});

describe('Nav', () => {
  it('links to the main pages and marks the current one', () => {
    render(Nav, { username: 'ana', onlogout: () => {}, runningOps: 0, current: '/settings' });
    const nav = screen.getByRole('navigation', { name: 'Main' });
    expect(nav).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Stacks' }).getAttribute('href')).toBe('/');
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/stacks/new');
    expect(screen.getByRole('link', { name: 'Import' }).getAttribute('href')).toBe('/stacks/import');
    expect(screen.getByRole('link', { name: 'Settings' }).getAttribute('aria-current')).toBe('page');
    expect(screen.getByRole('link', { name: 'Stacks' }).getAttribute('aria-current')).toBeNull();
  });

  it('shows running operations, the user and a working log out', async () => {
    const onlogout = vi.fn();
    render(Nav, { username: 'ana', onlogout, runningOps: 2, current: '/' });
    expect(screen.getByText(/2 running/)).toBeTruthy();
    expect(screen.getByText('ana')).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Log out' }));
    expect(onlogout).toHaveBeenCalledTimes(1);
  });
});
