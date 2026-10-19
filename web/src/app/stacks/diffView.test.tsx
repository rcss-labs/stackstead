import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { DiffView } from './DiffView';

afterEach(cleanup);

describe('DiffView with the loading fallback', () => {
  it('is a labelled group showing the modified text until the editor chunk loads', () => {
    render(<DiffView original={'a: 1\n'} modified={'a: 2\n'} ariaLabel="Changes" />);
    const group = screen.getByRole('group', { name: 'Changes' });
    expect(group.textContent).toContain('a: 2');
    expect(group.textContent).toContain('Diff unavailable (loading); showing your draft.');
  });
});

describe('DiffView with real CodeMirror (jsdom)', () => {
  it('marks a one-line change: added line and removed text are in the unified view', async () => {
    render(<DiffView original={'a: 1\nb: 2\n'} modified={'a: 1\nb: 3\n'} ariaLabel="Changes" />);
    await waitFor(() => expect(document.querySelector('.cm-editor')).not.toBeNull());
    const group = screen.getByRole('group', { name: 'Changes' });
    expect(group.textContent).toContain('b: 3');
    expect(group.querySelector('.cm-deletedChunk')?.textContent).toContain('b: 2');
    expect(group.querySelector('.cm-changedLine')).not.toBeNull();
    expect(group.querySelector('.cm-content')?.getAttribute('aria-label')).toBe('Changes (unified diff)');
  });

  it('destroys the view on unmount', async () => {
    const { unmount } = render(<DiffView original={'a\n'} modified={'b\n'} ariaLabel="Changes" />);
    await waitFor(() => expect(document.querySelector('.cm-editor')).not.toBeNull());
    unmount();
    expect(document.querySelector('.cm-editor')).toBeNull();
  });

  it('rebuilds when either side changes', async () => {
    const { rerender } = render(<DiffView original={'a\n'} modified={'b\n'} ariaLabel="Changes" />);
    await waitFor(() => expect(document.querySelectorAll('.cm-editor')).toHaveLength(1));
    rerender(<DiffView original={'a\n'} modified={'c\n'} ariaLabel="Changes" />);
    await waitFor(() => expect(document.querySelector('.cm-content')?.textContent).toContain('c'));
    expect(document.querySelectorAll('.cm-editor')).toHaveLength(1);
  });
});
