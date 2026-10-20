import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode, useState } from 'react';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get, writable } from '$lib/store';
import type { Change, StackSummary } from '$lib/api/types';
import { startStacks, stacks } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import CommandPalette from './CommandPalette';
import { PaletteHost, resetPalettePrefetchForTests } from './PaletteHost';

type PaletteModule = { default: typeof CommandPalette };

function stack(name: string): StackSummary {
  return {
    name,
    path: `/srv/${name}/compose.yaml`,
    project: name,
    origin: 'managed',
    status: 'running',
    serviceCount: 1,
    running: 1,
    health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
    ports: [],
  };
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

let stopStacks: () => void;
let setOpen: (open: boolean) => void;

beforeEach(async () => {
  resetPalettePrefetchForTests();
  stopStacks = startStacks({
    list: async () => [stack('redis'), stack('web')],
    changes: writable<(Change & { resync?: true }) | null>(null),
  });
  await waitFor(() => expect(get(stacks).items).toHaveLength(2));
});

afterEach(async () => {
  cleanup();
  stopStacks();
  toasts.reset();
  vi.useRealTimers();
  vi.restoreAllMocks();
  // useStore releases its store subscriptions on the next task after unmount.
  await new Promise((r) => setTimeout(r, 5));
});

function Harness({ load, onClose = () => {}, onNavigate }: { load: () => Promise<PaletteModule>; onClose?: () => void; onNavigate?: (href: string) => void }) {
  const [open, set] = useState(false);
  setOpen = set;
  return (
    <MemoryRouter>
      <button>Opener</button>
      <PaletteHost
        open={open}
        runningOps={[]}
        onClose={() => {
          onClose();
          set(false);
        }}
        onNavigate={onNavigate}
        load={load}
      />
    </MemoryRouter>
  );
}

describe('PaletteHost', () => {
  it('does not load the palette until the first open', async () => {
    const load = vi.fn(async () => ({ default: CommandPalette }));
    render(<Harness load={load} />);
    expect(load).not.toHaveBeenCalled();
    await act(async () => setOpen(true));
    expect(await screen.findByRole('listbox', { name: 'Results' })).toBeTruthy();
    expect(load).toHaveBeenCalledTimes(1);
    await act(async () => setOpen(false));
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('keys typed before the palette module arrives seed its query', async () => {
    const gate = deferred<PaletteModule>();
    const load = vi.fn(() => gate.promise);
    const onNavigate = vi.fn();
    const user = userEvent.setup();
    render(<Harness load={load} onNavigate={onNavigate} />);
    await act(async () => setOpen(true));
    const fallback = screen.getByRole('textbox', { name: 'Search or run a command' });
    await user.type(fallback, 'red');
    await user.keyboard('{Enter}');
    expect(onNavigate).not.toHaveBeenCalled();
    await act(async () => gate.resolve({ default: CommandPalette }));
    const list = await screen.findByRole('listbox', { name: 'Results' });
    const input = screen.getByRole('combobox', { name: 'Search or run a command' }) as HTMLInputElement;
    expect(input.value).toBe('red');
    expect(input.selectionStart).toBe(3);
    expect(document.activeElement).toBe(input);
    expect(within(list).getByText('redis')).toBeTruthy();
    expect(within(list).queryByText('web')).toBeNull();
    expect(screen.queryByRole('textbox')).toBeNull();
  });

  it('Escape while the module is loading closes the host and returns focus to the opener', async () => {
    const gate = deferred<PaletteModule>();
    const load = vi.fn(() => gate.promise);
    const onClose = vi.fn();
    const user = userEvent.setup();
    render(<Harness load={load} onClose={onClose} />);
    const opener = screen.getByRole('button', { name: 'Opener' });
    opener.focus();
    await act(async () => setOpen(true));
    await user.keyboard('{Escape}');
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(load).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('textbox')).toBeNull();
    expect(document.activeElement).toBe(opener);
  });

  it('a failed first load toasts, closes and retries on the next open', async () => {
    const load = vi
      .fn<() => Promise<PaletteModule>>()
      .mockRejectedValueOnce(new Error('Failed to fetch dynamically imported module'))
      .mockResolvedValue({ default: CommandPalette });
    const onClose = vi.fn();
    render(<Harness load={load} onClose={onClose} />);
    await act(async () => setOpen(true));
    await waitFor(() => expect(get(toasts).some((t) => t.kind === 'danger' && t.message === 'Could not open the command palette')).toBe(true));
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('dialog')).toBeNull();
    await act(async () => setOpen(true));
    expect(await screen.findByRole('listbox', { name: 'Results' })).toBeTruthy();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('prefetches the palette once on idle without opening it', async () => {
    vi.useFakeTimers();
    const load = vi.fn(async () => ({ default: CommandPalette }));
    render(
      <StrictMode>
        <Harness load={load} />
      </StrictMode>,
    );
    await act(async () => {
      vi.advanceTimersByTime(1500);
    });
    expect(load).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('dialog')).toBeNull();
    await act(async () => setOpen(true));
    expect(screen.getByRole('listbox', { name: 'Results' })).toBeTruthy();
    expect(screen.queryByRole('textbox')).toBeNull();
    expect(load).toHaveBeenCalledTimes(1);
  });

  it('a failed prefetch is silent and a later open loads again', async () => {
    vi.useFakeTimers();
    const load = vi
      .fn<() => Promise<PaletteModule>>()
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValue({ default: CommandPalette });
    render(<Harness load={load} />);
    await act(async () => {
      vi.advanceTimersByTime(1500);
    });
    expect(load).toHaveBeenCalledTimes(1);
    expect(get(toasts)).toHaveLength(0);
    await act(async () => setOpen(true));
    expect(screen.getByRole('listbox', { name: 'Results' })).toBeTruthy();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('a prefetch that fails while the palette is open closes it with the chunk-error toast, and the next open retries', async () => {
    vi.useFakeTimers();
    const gate = deferred<PaletteModule>();
    const load = vi
      .fn<() => Promise<PaletteModule>>()
      .mockImplementationOnce(() => gate.promise)
      .mockResolvedValue({ default: CommandPalette });
    const onClose = vi.fn();
    render(<Harness load={load} onClose={onClose} />);
    await act(async () => {
      vi.advanceTimersByTime(1500);
    });
    await act(async () => setOpen(true));
    expect(screen.getByRole('textbox', { name: 'Search or run a command' })).toBeTruthy();
    await act(async () => gate.reject(new Error('Failed to fetch dynamically imported module')));
    expect(get(toasts).some((t) => t.kind === 'danger' && t.message === 'Could not open the command palette')).toBe(true);
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('dialog')).toBeNull();
    await act(async () => setOpen(true));
    expect(screen.getByRole('listbox', { name: 'Results' })).toBeTruthy();
    expect(load).toHaveBeenCalledTimes(2);
  });

  it('a click on the backdrop closes the loading stand-in, which is named by a hidden heading', async () => {
    const gate = deferred<PaletteModule>();
    const onClose = vi.fn();
    render(<Harness load={() => gate.promise} onClose={onClose} />);
    await act(async () => setOpen(true));
    const dialog = screen.getByRole('dialog', { name: 'Command palette' });
    expect(within(dialog).getByRole('heading', { name: 'Command palette' })).toBeTruthy();
    await userEvent.setup().click(dialog.parentElement as HTMLElement);
    expect(onClose).toHaveBeenCalledTimes(1);
  });
});
