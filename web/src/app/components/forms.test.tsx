import { render, screen, waitFor } from '@testing-library/react';
import { createRoot } from 'react-dom/client';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';
import { Field, type FieldControlProps } from './Field';
import { ListEditor } from './ListEditor';
import { Tabs } from './Tabs';

describe('Tabs badges', () => {
  it('renders badges after the label and keeps roving tabindex', () => {
    render(<Tabs tabs={[{ id: 'a', label: 'Alpha', badge: 3 }, { id: 'b', label: 'Beta' }]} selected="a" onSelect={() => {}} />);
    const alpha = screen.getByRole('tab', { name: /^Alpha/ });
    expect(alpha.textContent).toBe('Alpha3');
    expect(screen.getByRole('tab', { name: 'Alpha, 3' })).toBe(alpha);
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
    render(
      <Tabs tabs={tabs} selected="b" onSelect={() => {}} ariaLabel="Stack sections">
        {() => <p>panel body</p>}
      </Tabs>,
    );
    expect(screen.getByRole('tablist', { name: 'Stack sections' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Beta' }).getAttribute('aria-selected')).toBe('true');
    expect(screen.getByRole('tab', { name: 'Alpha' }).getAttribute('aria-selected')).toBe('false');
    expect(screen.getByRole('tab', { name: 'Alpha' }).getAttribute('tabindex')).toBe('-1');
    expect(screen.getByRole('tabpanel', { name: 'Beta' }).textContent).toContain('panel body');
  });

  it('manual activation (default): arrows move focus only; Enter and Space select', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<Tabs tabs={tabs} selected="a" onSelect={onSelect} />);
    screen.getByRole('tab', { name: 'Alpha' }).focus();
    await user.keyboard('{ArrowRight}');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Beta' }));
    expect(onSelect).not.toHaveBeenCalled();
    await user.keyboard('{Enter}');
    expect(onSelect).toHaveBeenLastCalledWith('b');
    await user.keyboard('{ArrowRight}');
    await user.keyboard(' ');
    expect(onSelect).toHaveBeenLastCalledWith('c');
    await user.keyboard('{ArrowRight}');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Alpha' }));
    await user.keyboard('{End}');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Gamma' }));
    await user.keyboard('{Home}');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Alpha' }));
  });

  it('automatic activation: arrows, Home and End select and focus, wrapping around', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<Tabs tabs={tabs} selected="a" onSelect={onSelect} activation="auto" />);
    screen.getByRole('tab', { name: 'Alpha' }).focus();
    await user.keyboard('{ArrowRight}');
    expect(onSelect).toHaveBeenLastCalledWith('b');
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Beta' }));
    await user.keyboard('{ArrowLeft}{ArrowLeft}');
    expect(onSelect).toHaveBeenLastCalledWith('c');
    await user.keyboard('{Home}');
    expect(onSelect).toHaveBeenLastCalledWith('a');
    await user.keyboard('{End}');
    expect(onSelect).toHaveBeenLastCalledWith('c');
  });

  it('does not move focus when onSelect refuses the change', async () => {
    const user = userEvent.setup();
    render(<Tabs tabs={tabs} selected="a" onSelect={() => false} activation="auto" />);
    const alpha = screen.getByRole('tab', { name: 'Alpha' });
    alpha.focus();
    await user.keyboard('{ArrowRight}');
    expect(document.activeElement).toBe(alpha);
  });

  it('selects on click', async () => {
    const onSelect = vi.fn();
    render(<Tabs tabs={tabs} selected="a" onSelect={onSelect} />);
    await userEvent.setup().click(screen.getByRole('tab', { name: 'Gamma' }));
    expect(onSelect).toHaveBeenCalledWith('c');
  });
});

describe('Field', () => {
  const describedText = (el: HTMLElement) =>
    (el.getAttribute('aria-describedby') ?? '')
      .split(' ')
      .map((id) => document.getElementById(id)?.textContent)
      .join(' ');
  const control = (c: FieldControlProps) => <input {...c} />;

  // A control revealed after mount, spread from the same bag.
  function Fixture({ error, reveal, mono }: { error?: string; reveal: boolean; mono?: boolean }) {
    return (
      <Field label="Name" id="name" error={error} mono={mono} hint="Letters only">
        {(c) => (reveal ? <input {...c} /> : null)}
      </Field>
    );
  }

  it('associates the label with the control', () => {
    render(<Field label="Name" id="name">{control}</Field>);
    expect(screen.getByLabelText('Name').id).toBe('name');
  });

  it('passes aria-describedby for the hint and aria-invalid for an initial error', () => {
    render(<Field label="Name" id="name" hint="Letters only" error="Required">{control}</Field>);
    const el = screen.getByLabelText('Name');
    expect(el.getAttribute('aria-invalid')).toBe('true');
    expect(describedText(el)).toContain('Letters only');
    expect(describedText(el)).toContain('Required');
  });

  it('wires aria-invalid on a control revealed after mount', async () => {
    const { rerender } = render(<Fixture error="Required" reveal={false} />);
    expect(screen.queryByLabelText('Name')).toBeNull();
    rerender(<Fixture error="Required" reveal />);
    const el = await screen.findByLabelText('Name');
    expect(el.getAttribute('aria-invalid')).toBe('true');
    expect(describedText(el)).toContain('Required');
    rerender(<Fixture error={undefined} reveal />);
    await waitFor(() => expect(el.getAttribute('aria-invalid')).toBeNull());
  });

  it('tells the control it is read-only and explains why', () => {
    render(<Field label="Image" id="name" readonly readonlyReason="Set by an anchor">{control}</Field>);
    const el = screen.getByLabelText('Image') as HTMLInputElement;
    expect(el.readOnly).toBe(true);
    expect(describedText(el)).toContain('Set by an anchor');
  });

  it('mono applies to the control through the wrapper', async () => {
    const { rerender } = render(<Fixture reveal />);
    const wrapper = (await screen.findByText('Name')).parentElement!;
    expect(wrapper.getAttribute('data-mono')).toBeNull();
    rerender(<Fixture reveal mono />);
    expect(wrapper.getAttribute('data-mono')).toBe('true');
  });

  it('never clears a read-only control the caller made read-only itself', () => {
    render(<Field label="Fixed" id="name">{(c) => <input id={c.id} readOnly />}</Field>);
    expect((screen.getByLabelText('Fixed') as HTMLInputElement).readOnly).toBe(true);
  });
});

describe('ListEditor', () => {
  const renderRow = (item: string, index: number, update: (value: string) => void) => (
    <input aria-label={`Value ${index + 1}`} value={item} onChange={(e) => update(e.target.value)} />
  );

  // The parent owns the items and applies every change, like a real form.
  function Harness({ initial, onChange, ...extra }: { initial: string[]; onChange: (items: string[]) => void } & Partial<React.ComponentProps<typeof ListEditor<string>>>) {
    const [items, setItems] = useState(initial);
    return (
      <ListEditor<string>
        items={items}
        addLabel="Add item"
        newItem={() => ''}
        row={renderRow}
        onChange={(next) => {
          onChange(next);
          setItems(next);
        }}
        {...extra}
      />
    );
  }

  it('adds a row and focuses its first input', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={['a']} onChange={onChange} />);
    await user.click(screen.getByRole('button', { name: 'Add item' }));
    expect(onChange).toHaveBeenCalledWith(['a', '']);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Value 2')));
  });

  it('removing a row focuses the previous row; removing the first focuses the next', async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(<Harness initial={['a', 'b', 'c']} onChange={onChange} />);
    await user.click(screen.getByRole('button', { name: 'Remove item 3' }));
    expect(onChange).toHaveBeenLastCalledWith(['a', 'b']);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Value 2')));
    await user.click(screen.getByRole('button', { name: 'Remove item 1' }));
    expect(onChange).toHaveBeenLastCalledWith(['b']);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Value 1')));
  });

  it('removing the last remaining row focuses the add button', async () => {
    const user = userEvent.setup();
    render(<Harness initial={['a']} onChange={() => {}} />);
    await user.click(screen.getByRole('button', { name: 'Remove item 1' }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add item' })));
  });

  it('links each row error to its row via aria-describedby', () => {
    render(<Harness initial={['a', 'b']} onChange={() => {}} errors={{ 1: 'Invalid port' }} />);
    const row = screen.getByLabelText('Value 2').closest('[aria-describedby]') as HTMLElement;
    expect(document.getElementById(row.getAttribute('aria-describedby')!)?.textContent).toContain('Invalid port');
  });

  it('does not steal focus later when the parent ignored the change', async () => {
    const user = userEvent.setup();
    const props = { addLabel: 'Add item', newItem: () => '', row: renderRow, onChange: () => {} };
    const { rerender } = render(<ListEditor<string> items={['a']} {...props} />); // parent refuses
    await user.click(screen.getByRole('button', { name: 'Add item' }));
    screen.getByRole('button', { name: 'Add item' }).focus();
    rerender(<ListEditor<string> items={['a', 'b']} {...props} />);
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add item' }));
  });

  it('moves focus after add and remove with real browser event flushing (outside act)', async () => {
    const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
    const previous = g.IS_REACT_ACT_ENVIRONMENT;
    g.IS_REACT_ACT_ENVIRONMENT = false;
    const container = document.createElement('div');
    document.body.append(container);
    const root = createRoot(container);
    const click = (el: Element) => el.dispatchEvent(new MouseEvent('click', { bubbles: true }));
    const macrotask = () => new Promise((resolve) => setTimeout(resolve, 0));
    try {
      root.render(<Harness initial={['a']} onChange={() => {}} />);
      await macrotask();
      click(container.querySelector('button[aria-label="Add item"]')!);
      await macrotask();
      expect(document.activeElement).toBe(container.querySelector('input[aria-label="Value 2"]'));
      click(container.querySelector('button[aria-label="Remove item 2"]')!);
      await macrotask();
      expect(document.activeElement).toBe(container.querySelector('input[aria-label="Value 1"]'));
    } finally {
      root.unmount();
      container.remove();
      g.IS_REACT_ACT_ENVIRONMENT = previous;
    }
  });

  it('shows per-row errors and hides add/remove when read-only', () => {
    const { unmount } = render(<Harness initial={['a', 'b']} onChange={() => {}} errors={{ 1: 'Invalid port' }} />);
    expect(screen.getByText('Invalid port')).toBeTruthy();
    unmount();
    render(<Harness initial={['a']} onChange={() => {}} readonly />);
    expect(screen.queryByRole('button', { name: 'Add item' })).toBeNull();
    expect(screen.queryByRole('button', { name: /Remove/ })).toBeNull();
  });
});
