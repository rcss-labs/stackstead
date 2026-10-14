import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { describe, expect, it, vi } from 'vitest';
import type { EditorOptions } from './cm';

const created: { opts: EditorOptions; destroy: ReturnType<typeof vi.fn>; setValue: ReturnType<typeof vi.fn>; current: string }[] = [];

// jsdom cannot lay out CodeMirror, so the editor factory is stubbed; the real one is smoke-tested in cm.test.ts.
vi.mock('./cm', () => ({
  createEditor: (opts: EditorOptions) => {
    const rec = { opts, destroy: vi.fn(), setValue: vi.fn(), current: opts.value };
    rec.setValue.mockImplementation((v: string) => (rec.current = v));
    created.push(rec);
    return { getValue: () => rec.current, setValue: rec.setValue, goToLine: vi.fn(), focus: vi.fn(), destroy: rec.destroy };
  },
}));

import YamlEditor from './YamlEditor.svelte';

describe('YamlEditor', () => {
  it('renders a working textarea synchronously, then swaps to CodeMirror', async () => {
    created.length = 0;
    const onchange = vi.fn();
    render(YamlEditor, { value: 'a: 1', ariaLabel: 'Compose file', onchange });
    const ta = screen.getByRole('textbox', { name: 'Compose file' }) as HTMLTextAreaElement;
    expect(ta.tagName).toBe('TEXTAREA');
    expect(ta.value).toBe('a: 1');
    // Synchronous input: the chunk may swap the textarea out at any microtask.
    await fireEvent.input(ta, { target: { value: 'a: 1x' } });
    expect(onchange).toHaveBeenLastCalledWith('a: 1x');

    await waitFor(() => expect(created).toHaveLength(1));
    await waitFor(() => expect(screen.queryByRole('textbox')).toBeNull());
    expect(created[0].opts).toMatchObject({ readonly: false, ariaLabel: 'Compose file' });
  });

  it('forwards CodeMirror changes to onchange', async () => {
    created.length = 0;
    const onchange = vi.fn();
    render(YamlEditor, { value: '', ariaLabel: 'x', onchange });
    await waitFor(() => expect(created).toHaveLength(1));
    created[0].opts.onchange('b: 2');
    expect(onchange).toHaveBeenCalledWith('b: 2');
  });

  it('pushes parent value changes into CodeMirror', async () => {
    created.length = 0;
    const { rerender } = render(YamlEditor, { value: 'a', ariaLabel: 'x' });
    await waitFor(() => expect(created).toHaveLength(1));
    await rerender({ value: 'z', ariaLabel: 'x' });
    await waitFor(() => expect(created[0].setValue).toHaveBeenCalledWith('z'));
  });

  it('passes readonly to the textarea and to CodeMirror', async () => {
    created.length = 0;
    render(YamlEditor, { value: 'a', readonly: true, ariaLabel: 'x' });
    expect((screen.getByRole('textbox') as HTMLTextAreaElement).readOnly).toBe(true);
    await waitFor(() => expect(created[0]?.opts.readonly).toBe(true));
  });

  it('destroys the EditorView on unmount', async () => {
    created.length = 0;
    const { unmount } = render(YamlEditor, { value: 'a', ariaLabel: 'x' });
    await waitFor(() => expect(created).toHaveLength(1));
    unmount();
    expect(created[0].destroy).toHaveBeenCalledTimes(1);
  });
});
