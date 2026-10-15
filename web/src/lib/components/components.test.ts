import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { createRawSnippet } from 'svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import { navigate, route } from '../router';
import { toasts } from '../stores/toasts';
import Banner from './Banner.svelte';
import Button from './Button.svelte';
import ConfirmDialog from './ConfirmDialog.svelte';
import CopyButton from './CopyButton.svelte';
import Dialog from './Dialog.svelte';
import EmptyState from './EmptyState.svelte';
import Field from './Field.svelte';
import FieldFixture from './FieldFixture.test-fixture.svelte';
import ListEditor from './ListEditor.svelte';
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

  it('uses role=alert for danger toasts only', async () => {
    render(Toasts);
    toasts.push({ kind: 'info', message: 'fyi', timeoutMs: 0 });
    toasts.push({ kind: 'danger', message: 'broken' });
    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('broken'));
    expect(screen.getAllByRole('alert')).toHaveLength(1);
  });

  it('lives outside #app so a modal dialog does not make it inert, and stays dismissable', async () => {
    const root = document.createElement('div');
    root.id = 'app';
    document.body.append(root);
    render(Toasts, { target: root });
    const dlg = render(Dialog, { open: true, title: 'Modal', onclose: () => {}, children: html('<button>Ok</button>') });
    await waitFor(() => expect(root.inert).toBe(true));
    toasts.push({ kind: 'warning', message: 'while modal', timeoutMs: 0 });
    const region = await screen.findByRole('region', { name: 'Notifications' });
    expect(root.contains(region)).toBe(false);
    expect(region.closest('[inert]')).toBeNull();
    await userEvent.setup().click(screen.getByRole('button', { name: /dismiss/i }));
    await waitFor(() => expect(region.textContent).not.toContain('while modal'));
    dlg.unmount();
    root.remove();
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

  it('closes on Escape even when focus is outside the dialog', async () => {
    const onclose = vi.fn();
    render(Dialog, { open: true, title: 'T', onclose, children: body });
    (document.activeElement as HTMLElement | null)?.blur();
    await userEvent.setup().keyboard('{Escape}');
    expect(onclose).toHaveBeenCalledTimes(1);
  });

  it('closes only the topmost of nested dialogs per Escape, and ignores handled keys', async () => {
    const user = userEvent.setup();
    const outer = vi.fn();
    const inner = vi.fn();
    render(Dialog, { open: true, title: 'Outer', onclose: outer, children: body });
    const innerView = render(Dialog, { open: true, title: 'Inner', onclose: inner, children: body });
    await user.keyboard('{Escape}');
    expect(inner).toHaveBeenCalledTimes(1);
    expect(outer).not.toHaveBeenCalled();
    innerView.unmount();
    const handled = (e: KeyboardEvent) => e.preventDefault();
    window.addEventListener('keydown', handled, { capture: true, once: true });
    await user.keyboard('{Escape}');
    expect(outer).not.toHaveBeenCalled(); // already handled by someone else
    await user.keyboard('{Escape}');
    expect(outer).toHaveBeenCalledTimes(1);
  });

  it('does not focus hidden controls', async () => {
    render(Dialog, {
      open: true,
      title: 'T',
      onclose: () => {},
      children: html('<div><button hidden>Ghost</button><button>Real</button></div>'),
    });
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Real' })));
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
    const dlg = screen.getByRole('alertdialog', { name: 'Remove stack' });
    expect(dlg.textContent).toContain('This stops all services.');
    expect(document.getElementById(dlg.getAttribute('aria-describedby')!)?.textContent).toBe('This stops all services.');
    await user.click(screen.getByRole('button', { name: 'Remove' }));
    expect(onconfirm).toHaveBeenCalledTimes(1);
    await user.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(oncancel).toHaveBeenCalledTimes(1);
  });

  it('renders extra content between the message and the actions', () => {
    render(ConfirmDialog, {
      open: true,
      title: 'Remove',
      message: 'm',
      confirmLabel: 'Remove',
      onconfirm: () => {},
      oncancel: () => {},
      children: html('<label><input type="checkbox" /> Also remove volumes</label>'),
    });
    expect(screen.getByRole('checkbox', { name: 'Also remove volumes' })).toBeTruthy();
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
  type Aria = { id: string; 'aria-describedby'?: string; 'aria-invalid'?: 'true'; readonly: boolean };
  // The control is rendered by the caller from the ARIA bag the Field passes in.
  const control = createRawSnippet<[Aria]>((a) => ({
    render: () => {
      const v = a();
      return `<input id="${v.id}"${v['aria-describedby'] ? ` aria-describedby="${v['aria-describedby']}"` : ''}${
        v['aria-invalid'] ? ' aria-invalid="true"' : ''
      }${v.readonly ? ' readonly' : ''} />`;
    },
  }));
  const fixedReadonly = createRawSnippet<[Aria]>((a) => ({
    render: () => `<input id="${a().id}" readonly />`,
  }));
  const describedText = (el: HTMLElement) =>
    (el.getAttribute('aria-describedby') ?? '')
      .split(' ')
      .map((id) => document.getElementById(id)?.textContent)
      .join(' ');

  it('associates the label with the control', () => {
    render(Field, { label: 'Name', id: 'name', children: control });
    expect(screen.getByLabelText('Name').id).toBe('name');
  });

  it('passes aria-describedby for the hint and aria-invalid for an initial error', async () => {
    render(Field, { label: 'Name', id: 'name', hint: 'Letters only', error: 'Required', children: control });
    const el = screen.getByLabelText('Name');
    expect(el.getAttribute('aria-invalid')).toBe('true');
    expect(describedText(el)).toContain('Letters only');
    expect(describedText(el)).toContain('Required');
  });

  it('wires aria-invalid on a control revealed after mount', async () => {
    const { rerender } = render(FieldFixture, { error: 'Required', reveal: false });
    expect(screen.queryByLabelText('Name')).toBeNull();
    await rerender({ reveal: true });
    const el = await screen.findByLabelText('Name');
    expect(el.getAttribute('aria-invalid')).toBe('true');
    expect(describedText(el)).toContain('Required');
    await rerender({ error: undefined });
    await waitFor(() => expect(el.getAttribute('aria-invalid')).toBeNull());
  });

  it('tells the control it is read-only and explains why', () => {
    render(Field, { label: 'Image', id: 'name', readonly: true, readonlyReason: 'Set by an anchor', children: control });
    const el = screen.getByLabelText('Image') as HTMLInputElement;
    expect(el.readOnly).toBe(true);
    expect(describedText(el)).toContain('Set by an anchor');
  });

  it('mono applies to the control through the wrapper class', async () => {
    const { rerender } = render(FieldFixture, { reveal: true });
    const wrapper = (await screen.findByLabelText('Name')).closest('.field')!;
    expect(wrapper.classList.contains('mono')).toBe(false);
    await rerender({ mono: true });
    expect(wrapper.classList.contains('mono')).toBe(true);
  });

  it('never clears a read-only control the caller made read-only itself', () => {
    render(Field, { label: 'Fixed', id: 'name', children: fixedReadonly });
    expect((screen.getByLabelText('Fixed') as HTMLInputElement).readOnly).toBe(true);
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

  it('forwards rest props such as aria-label and id', () => {
    render(Button, { variant: 'ghost', 'aria-label': 'Close panel', id: 'close-btn', children: html('<span>×</span>') });
    const btn = screen.getByRole('button', { name: 'Close panel' });
    expect(btn.id).toBe('close-btn');
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

describe('Button v2', () => {
  it('applies size classes and defaults to md', () => {
    render(Button, { variant: 'secondary', size: 'sm', children: html('<span>Small</span>') });
    expect(screen.getByRole('button', { name: 'Small' }).classList.contains('sm')).toBe(true);
    render(Button, { variant: 'secondary', children: html('<span>Default</span>') });
    expect(screen.getByRole('button', { name: 'Default' }).classList.contains('md')).toBe(true);
  });

  it('icon button keeps aria-label as its name', () => {
    render(Button, { variant: 'ghost', icon: true, 'aria-label': 'Refresh', children: html('<span aria-hidden="true">↻</span>') });
    const btn = screen.getByRole('button', { name: 'Refresh' });
    expect(btn.classList.contains('icon')).toBe(true);
  });

  it('danger is outlined by default and filled inside ConfirmDialog', () => {
    render(Button, { variant: 'danger', children: html('<span>Delete</span>') });
    expect(screen.getByRole('button', { name: 'Delete' }).classList.contains('confirm')).toBe(false);
    render(ConfirmDialog, { open: true, title: 'Sure?', message: 'm', confirmLabel: 'Remove it', danger: true, onconfirm: () => {}, oncancel: () => {} });
    expect(screen.getByRole('button', { name: 'Remove it' }).classList.contains('confirm')).toBe(true);
  });
});

describe('StatusBadge', () => {
  it.each([
    ['running', 'running', 'success'],
    ['succeeded', 'succeeded', 'success'],
    ['healthy', 'healthy', 'success'],
    ['partial', 'partial', 'warning'],
    ['timed_out', 'timed out', 'warning'],
    ['starting', 'starting', 'warning'],
    ['restarting', 'restarting', 'warning'],
    ['stopped', 'stopped', 'muted'],
    ['cancelled', 'cancelled', 'muted'],
    ['unknown', 'unknown', 'muted'],
    ['exited', 'exited', 'muted'],
    ['none', 'none', 'muted'],
    ['unavailable', 'unavailable', 'danger'],
    ['failed', 'failed', 'danger'],
    ['unhealthy', 'unhealthy', 'danger'],
  ] as const)('%s renders %s in the %s tone', (status, text, tone) => {
    render(StatusBadge, { status });
    const badge = screen.getByText(text);
    expect(badge.classList.contains(tone)).toBe(true);
  });

  it('renders an unknown state muted with the raw word', () => {
    render(StatusBadge, { status: 'removing' });
    expect(screen.getByText('removing').classList.contains('muted')).toBe(true);
  });

  it('renders no decorative glyph', () => {
    const { container } = render(StatusBadge, { status: 'running' });
    expect(container.querySelector('[aria-hidden]')).toBeNull();
    expect(container.textContent).toBe('running');
  });
});

describe('Tabs badges', () => {
  it('renders badges after the label and keeps roving tabindex', () => {
    render(Tabs, {
      tabs: [{ id: 'a', label: 'Alpha', badge: 3 }, { id: 'b', label: 'Beta' }],
      selected: 'a',
      onselect: () => {},
    });
    const alpha = screen.getByRole('tab', { name: /^Alpha/ });
    expect(alpha.textContent).toMatch(/^Alpha/);
    expect(alpha.querySelector('.badge')?.textContent).toBe('3');
    expect(screen.getByRole('tab', { name: 'Alpha, 3' })).toBe(alpha);
    expect(alpha.querySelector('[aria-hidden]')).toBeNull();
    expect(alpha.getAttribute('tabindex')).toBe('0');
    expect(screen.getByRole('tab', { name: 'Beta' }).getAttribute('tabindex')).toBe('-1');
  });
});

describe('Tabs', () => {
  const tabs = [
    { id: 'a', label: 'Alpha' },
    { id: 'b', label: 'Beta' },
    { id: 'c', label: 'Gamma' },
  ];

  it('exposes a labelled tablist, tabs and a tabpanel for the selected tab', () => {
    render(Tabs, { tabs, selected: 'b', onselect: () => {}, ariaLabel: 'Stack sections', children: html('<p>panel body</p>') });
    expect(screen.getByRole('tablist', { name: 'Stack sections' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Beta' }).getAttribute('aria-selected')).toBe('true');
    expect(screen.getByRole('tab', { name: 'Alpha' }).getAttribute('aria-selected')).toBe('false');
    expect(screen.getByRole('tab', { name: 'Alpha' }).getAttribute('tabindex')).toBe('-1');
    expect(screen.getByRole('tabpanel', { name: 'Beta' }).textContent).toContain('panel body');
  });

  it('manual activation (default): arrows move focus only; Enter and Space select', async () => {
    const user = userEvent.setup();
    const onselect = vi.fn();
    render(Tabs, { tabs, selected: 'a', onselect });
    screen.getByRole('tab', { name: 'Alpha' }).focus();
    await user.keyboard('{ArrowRight}');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Beta' }));
    expect(onselect).not.toHaveBeenCalled();
    await user.keyboard('{Enter}');
    expect(onselect).toHaveBeenLastCalledWith('b');
    await user.keyboard('{ArrowRight}');
    await user.keyboard(' ');
    expect(onselect).toHaveBeenLastCalledWith('c');
    await user.keyboard('{ArrowRight}');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Alpha' }));
    await user.keyboard('{End}');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Gamma' }));
    await user.keyboard('{Home}');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Alpha' }));
  });

  it('automatic activation: arrows, Home and End select and focus, wrapping around', async () => {
    const user = userEvent.setup();
    const onselect = vi.fn();
    render(Tabs, { tabs, selected: 'a', onselect, activation: 'auto' });
    screen.getByRole('tab', { name: 'Alpha' }).focus();
    await user.keyboard('{ArrowRight}');
    expect(onselect).toHaveBeenLastCalledWith('b');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Beta' }));
    await user.keyboard('{ArrowLeft}{ArrowLeft}');
    expect(onselect).toHaveBeenLastCalledWith('c');
    await user.keyboard('{Home}');
    expect(onselect).toHaveBeenLastCalledWith('a');
    await user.keyboard('{End}');
    expect(onselect).toHaveBeenLastCalledWith('c');
  });

  it('does not move focus when onselect refuses the change', async () => {
    const user = userEvent.setup();
    render(Tabs, { tabs, selected: 'a', onselect: () => false, activation: 'auto' });
    const alpha = screen.getByRole('tab', { name: 'Alpha' });
    alpha.focus();
    await user.keyboard('{ArrowRight}');
    expect(document.activeElement).toBe(alpha);
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
    render: () => `<input aria-label="Value ${index() + 1}" value="${item()}" />`,
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
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Value 2')));
  });

  it('removing a row focuses the previous row; removing the first focuses the next', async () => {
    const user = userEvent.setup();
    const { onchange } = setup(['a', 'b', 'c']);
    await user.click(screen.getByRole('button', { name: 'Remove item 3' }));
    expect(onchange).toHaveBeenLastCalledWith(['a', 'b']);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Value 2')));
    await user.click(screen.getByRole('button', { name: 'Remove item 1' }));
    expect(onchange).toHaveBeenLastCalledWith(['b']);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Value 1')));
  });

  it('removing the last remaining row focuses the add button', async () => {
    const user = userEvent.setup();
    setup(['a']);
    await user.click(screen.getByRole('button', { name: 'Remove item 1' }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add item' })));
  });

  it('links each row error to its row via aria-describedby', () => {
    setup(['a', 'b'], { errors: { 1: 'Invalid port' } });
    const row = screen.getByLabelText('Value 2').closest('[aria-describedby]') as HTMLElement;
    expect(document.getElementById(row.getAttribute('aria-describedby')!)?.textContent).toContain('Invalid port');
  });

  it('does not steal focus later when the parent ignored the change', async () => {
    const user = userEvent.setup();
    let items = ['a'];
    const { rerender } = render(ListEditor<string>, {
      items,
      addLabel: 'Add item',
      newItem: () => '',
      row,
      onchange: () => {}, // parent refuses
    });
    await user.click(screen.getByRole('button', { name: 'Add item' }));
    screen.getByRole('button', { name: 'Add item' }).focus();
    items = ['a', 'b'];
    await rerender({ items });
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add item' }));
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
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Copy' }));
  });

  it('says so when both copy paths fail', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
    (document as unknown as { execCommand: unknown }).execCommand = vi.fn().mockReturnValue(false);
    render(CopyButton, { text: 'abc' });
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Copy failed'));
  });

  it('re-announces on a repeat copy', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockResolvedValue(undefined) }, configurable: true });
    render(CopyButton, { text: 'abc' });
    const status = screen.getByRole('status');
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    await waitFor(() => expect(status.textContent).toContain('Copied'));
    let mutations = 0;
    const obs = new MutationObserver((m) => (mutations += m.length));
    obs.observe(status, { childList: true, characterData: true, subtree: true });
    await user.click(screen.getByRole('button', { name: 'Copy' }));
    await waitFor(() => expect(mutations).toBeGreaterThan(0));
    obs.disconnect();
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
