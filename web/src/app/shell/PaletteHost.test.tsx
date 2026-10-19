import { act, cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';

// The factory runs only when the palette module is first imported, so the flag records exactly when that happens.
const loaded = vi.hoisted(() => ({ value: false }));
vi.mock('./CommandPalette', () => {
  loaded.value = true;
  return { default: ({ open }: { open: boolean }) => (open ? <div role="dialog" aria-label="Command palette" /> : null) };
});

import { PaletteHost } from './PaletteHost';

afterEach(() => cleanup());

describe('PaletteHost', () => {
  it('does not import the palette module until the first open', async () => {
    const ui = (open: boolean) => (
      <MemoryRouter>
        <PaletteHost open={open} onClose={() => {}} />
      </MemoryRouter>
    );
    const { rerender } = render(ui(false));
    expect(loaded.value).toBe(false);
    rerender(ui(true));
    expect(await screen.findByRole('dialog', { name: 'Command palette' })).toBeTruthy();
    expect(loaded.value).toBe(true);
    await act(async () => rerender(ui(false)));
    expect(screen.queryByRole('dialog')).toBeNull();
  });
});
