import { render, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import YamlEditor from './YamlEditor.svelte';

// Real CodeMirror (no ./cm mock): this file checks the changed-line decorations.
afterEach(() => vi.useRealTimers());

const changedCount = (c: HTMLElement) => c.querySelectorAll('.cm-changed-line').length;

describe('YamlEditor changed lines', () => {
  it('marks changed lines 150 ms after an edit, against the original prop', async () => {
    const { container, rerender } = render(YamlEditor, { value: 'a: 1\nb: 2\n', original: 'a: 1\nb: 2\n', ariaLabel: 'x' });
    await waitFor(() => expect(container.querySelector('.cm-editor')).not.toBeNull());
    expect(changedCount(container)).toBe(0);

    vi.useFakeTimers();
    await rerender({ value: 'a: 1\nb: 3\nc: 4\n', original: 'a: 1\nb: 2\n', ariaLabel: 'x' });
    vi.advanceTimersByTime(100);
    expect(changedCount(container)).toBe(0);
    vi.advanceTimersByTime(60);
    expect(changedCount(container)).toBe(2);
    expect(container.querySelectorAll('.cm-changed-marker').length).toBe(2);

    // A new baseline (after a save) clears the marks.
    await rerender({ value: 'a: 1\nb: 3\nc: 4\n', original: 'a: 1\nb: 3\nc: 4\n', ariaLabel: 'x' });
    vi.advanceTimersByTime(160);
    expect(changedCount(container)).toBe(0);
  });

  it('marks nothing when there is no original', async () => {
    const { container, rerender } = render(YamlEditor, { value: 'a: 1\n', ariaLabel: 'x' });
    await waitFor(() => expect(container.querySelector('.cm-editor')).not.toBeNull());
    vi.useFakeTimers();
    await rerender({ value: 'a: 2\n', ariaLabel: 'x' });
    vi.advanceTimersByTime(200);
    expect(changedCount(container)).toBe(0);
  });
});
