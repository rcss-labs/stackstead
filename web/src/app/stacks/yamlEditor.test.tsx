import { createRef } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { EditorOptions } from '$lib/stacks/cm';

interface Recorded {
  opts: EditorOptions;
  destroy: ReturnType<typeof vi.fn>;
  setValue: ReturnType<typeof vi.fn>;
  setReadonly: ReturnType<typeof vi.fn>;
  focus: ReturnType<typeof vi.fn>;
  current: string;
}
const created: Recorded[] = [];

// jsdom cannot lay out CodeMirror, so the editor factory is stubbed; the real one is exercised in the changed-lines and diff tests.
vi.mock('$lib/stacks/cm', () => ({
  createEditor: (opts: EditorOptions) => {
    const rec: Recorded = { opts, destroy: vi.fn(), setValue: vi.fn(), setReadonly: vi.fn(), focus: vi.fn(), current: opts.value };
    rec.setValue.mockImplementation((v: string) => (rec.current = v));
    created.push(rec);
    return { getValue: () => rec.current, setValue: rec.setValue, setReadonly: rec.setReadonly, setOriginal: vi.fn(), goToLine: vi.fn(), focus: rec.focus, destroy: rec.destroy };
  },
}));

import { YamlEditor, type YamlEditorHandle } from './YamlEditor';

afterEach(() => {
  cleanup();
  created.length = 0;
});

describe('YamlEditor', () => {
  it('renders a working textarea synchronously, then swaps to CodeMirror', async () => {
    const onChange = vi.fn();
    render(<YamlEditor value="a: 1" ariaLabel="Compose file" onChange={onChange} />);
    const ta = screen.getByRole('textbox', { name: 'Compose file' }) as HTMLTextAreaElement;
    expect(ta.tagName).toBe('TEXTAREA');
    expect(ta.value).toBe('a: 1');
    fireEvent.change(ta, { target: { value: 'a: 1x' } });
    expect(onChange).toHaveBeenLastCalledWith('a: 1x');

    await waitFor(() => expect(created).toHaveLength(1));
    await waitFor(() => expect(screen.queryByRole('textbox')).toBeNull());
    expect(created[0].opts).toMatchObject({ readonly: false, ariaLabel: 'Compose file' });
  });

  it('forwards CodeMirror changes to onChange', async () => {
    const onChange = vi.fn();
    render(<YamlEditor value="" ariaLabel="x" onChange={onChange} />);
    await waitFor(() => expect(created).toHaveLength(1));
    created[0].opts.onchange('b: 2');
    expect(onChange).toHaveBeenCalledWith('b: 2');
  });

  it('pushes parent value changes into CodeMirror', async () => {
    const { rerender } = render(<YamlEditor value="a" ariaLabel="x" />);
    await waitFor(() => expect(created).toHaveLength(1));
    rerender(<YamlEditor value="z" ariaLabel="x" />);
    await waitFor(() => expect(created[0].setValue).toHaveBeenCalledWith('z'));
  });

  it('passes readonly to the textarea and to CodeMirror', async () => {
    render(<YamlEditor value="a" readonly ariaLabel="x" />);
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).readOnly).toBe(true);
    await waitFor(() => expect(created[0]?.opts.readonly).toBe(true));
  });

  it('destroys the EditorView on unmount', async () => {
    const { unmount } = render(<YamlEditor value="a" ariaLabel="x" />);
    await waitFor(() => expect(created).toHaveLength(1));
    unmount();
    expect(created[0].destroy).toHaveBeenCalledTimes(1);
  });

  it('keeps focus across the swap', async () => {
    render(<YamlEditor value="a" ariaLabel="x" />);
    const ta = screen.getByRole('textbox');
    ta.focus();
    expect(document.activeElement).toBe(ta);
    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0].focus).toHaveBeenCalled();
  });

  it('goToLine selects the line in the textarea fallback', () => {
    const ref = createRef<YamlEditorHandle>();
    render(<YamlEditor ref={ref} value={'ab\ncd\nef'} ariaLabel="x" />);
    act(() => ref.current?.goToLine(2));
    const ta = screen.getByRole('textbox') as HTMLTextAreaElement;
    expect(ta.selectionStart).toBe(3);
    expect(document.activeElement).toBe(ta);
  });
});

describe('YamlEditor late chunk', () => {
  it('does not create an editor when the chunk resolves after unmount', async () => {
    const { unmount } = render(<YamlEditor value="a" ariaLabel="x" />);
    unmount();
    await new Promise((r) => setTimeout(r, 20));
    expect(created).toHaveLength(0);
  });

  it('toggling readonly reaches CodeMirror without recreating it', async () => {
    const { rerender } = render(<YamlEditor value="a" ariaLabel="x" readonly={false} />);
    await waitFor(() => expect(created).toHaveLength(1));
    rerender(<YamlEditor value="a" ariaLabel="x" readonly />);
    await waitFor(() => expect(created[0].setReadonly).toHaveBeenCalledWith(true));
    expect(created).toHaveLength(1);
  });
});
