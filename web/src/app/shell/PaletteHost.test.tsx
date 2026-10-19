import { act, cleanup, render, screen, waitFor } from '@testing-library/react';
import { useState } from 'react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { toasts } from '$lib/stores/toasts';

// The factory runs only when the palette module is first imported, so the flag records exactly when that happens.
const loaded = vi.hoisted(() => ({ value: false }));
vi.mock('./CommandPalette', () => {
  loaded.value = true;
  return { default: ({ open }: { open: boolean }) => (open ? <div role="dialog" aria-label="Command palette" /> : null) };
});

import { PaletteHost } from './PaletteHost';

afterEach(() => {
  cleanup();
  toasts.reset();
  vi.restoreAllMocks();
});

describe('PaletteHost', () => {
  it('does not import the palette module until the first open', async () => {
    const ui = (open: boolean) => (
      <MemoryRouter>
        <PaletteHost open={open} runningOps={[]} onClose={() => {}} />
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

  it('closes and warns when the palette fails to load, and the next open retries the import', async () => {
    vi.spyOn(console, 'error').mockImplementation(() => {});
    const FakePalette = ({ open }: { open: boolean }) => (open ? <div role="dialog" aria-label="Command palette" /> : null);
    const load = vi
      .fn<() => Promise<{ default: typeof FakePalette }>>()
      .mockRejectedValueOnce(new Error('Failed to fetch dynamically imported module'))
      .mockResolvedValue({ default: FakePalette });
    let setOpen: (open: boolean) => void = () => {};
    function Harness() {
      const [open, set] = useState(false);
      setOpen = set;
      return <PaletteHost open={open} runningOps={[]} onClose={() => set(false)} load={load} />;
    }
    render(
      <MemoryRouter>
        <Harness />
      </MemoryRouter>,
    );
    await act(async () => setOpen(true));
    await waitFor(() => expect(get(toasts).some((t) => t.kind === 'danger' && t.message === 'Could not open the command palette')).toBe(true));
    expect(screen.queryByRole('dialog')).toBeNull();
    await act(async () => setOpen(true));
    expect(await screen.findByRole('dialog', { name: 'Command palette' })).toBeTruthy();
    expect(load).toHaveBeenCalledTimes(2);
  });
});
