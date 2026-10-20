import { StrictMode } from 'react';
import { act, cleanup, render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { YamlEditor } from './YamlEditor';

// Real CodeMirror (no cm mock): this file checks the changed-line decorations.
afterEach(() => {
  vi.useRealTimers();
  cleanup();
});

const changedCount = (c: HTMLElement) => c.querySelectorAll('.cm-changed-line').length;
const advance = (ms: number) => act(() => vi.advanceTimersByTime(ms));

describe('YamlEditor changed lines', () => {
  it('marks changed lines 150 ms after an edit, against the original prop', async () => {
    const { container, rerender } = render(<YamlEditor value={'a: 1\nb: 2\n'} original={'a: 1\nb: 2\n'} ariaLabel="x" />);
    await waitFor(() => expect(container.querySelector('.cm-editor')).not.toBeNull());
    expect(changedCount(container)).toBe(0);

    vi.useFakeTimers();
    rerender(<YamlEditor value={'a: 1\nb: 3\nc: 4\n'} original={'a: 1\nb: 2\n'} ariaLabel="x" />);
    advance(100);
    expect(changedCount(container)).toBe(0);
    advance(60);
    expect(changedCount(container)).toBe(2);
    expect(container.querySelectorAll('.cm-changed-marker').length).toBe(2);

    // A new baseline (after a save) clears the marks.
    rerender(<YamlEditor value={'a: 1\nb: 3\nc: 4\n'} original={'a: 1\nb: 3\nc: 4\n'} ariaLabel="x" />);
    advance(160);
    expect(changedCount(container)).toBe(0);
  });

  it('marks nothing when there is no original', async () => {
    const { container, rerender } = render(<YamlEditor value={'a: 1\n'} ariaLabel="x" />);
    await waitFor(() => expect(container.querySelector('.cm-editor')).not.toBeNull());
    vi.useFakeTimers();
    rerender(<YamlEditor value={'a: 2\n'} ariaLabel="x" />);
    advance(200);
    expect(changedCount(container)).toBe(0);
  });
});

describe('YamlEditor under StrictMode', () => {
  it('leaves exactly one editor mounted after the double effect run', async () => {
    const { container } = render(
      <StrictMode>
        <YamlEditor value={'a: 1\n'} ariaLabel="x" />
      </StrictMode>,
    );
    await waitFor(() => expect(container.querySelector('.cm-editor')).not.toBeNull());
    await act(() => new Promise((r) => setTimeout(r, 30)));
    expect(container.querySelectorAll('.cm-editor')).toHaveLength(1);
    expect(container.querySelector('textarea')).toBeNull();
  });
});
