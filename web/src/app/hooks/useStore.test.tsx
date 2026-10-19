import { StrictMode, Suspense, useLayoutEffect } from 'react';
import { act, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { derived, writable } from '$lib/store';
import { useStore } from './useStore';
import type { Readable } from '$lib/store';

function Probe({ store, onRender }: { store: Readable<unknown>; onRender?: () => void }) {
  const value = useStore(store);
  onRender?.();
  return <span data-testid="v">{String(value)}</span>;
}

// A writable whose start/stop notifier counts store-level subscriptions.
function countedWritable<T>(initial: T) {
  const stop = vi.fn();
  const start = vi.fn(() => stop);
  return { store: writable(initial, start), start, stop };
}

// The hook releases its store subscription on a timer after the last consumer leaves.
function flushRelease() {
  act(() => {
    vi.runAllTimers();
  });
}

afterEach(() => {
  vi.useRealTimers();
});

describe('useStore', () => {
  it('renders the current value on first render without a flash of undefined', () => {
    const store = writable<string>('x', (set) => {
      set('started');
    });
    const rendered: string[] = [];
    const { container } = render(<Probe store={store} onRender={() => rendered.push('render')} />);
    expect(container.textContent).toBe('started');
    expect(container.textContent).not.toContain('undefined');
    expect(rendered).toHaveLength(1);
  });

  it('re-renders on set and not on an equal primitive set', () => {
    const store = writable(1);
    const spy = vi.fn();
    const { container } = render(<Probe store={store} onRender={spy} />);
    const initial = spy.mock.calls.length;
    act(() => store.set(1));
    expect(spy.mock.calls.length).toBe(initial);
    act(() => store.set(2));
    expect(container.textContent).toBe('2');
    expect(spy.mock.calls.length).toBeGreaterThan(initial);
  });

  it('unsubscribes on unmount', () => {
    vi.useFakeTimers();
    const { store, start, stop } = countedWritable(0);
    const { unmount } = render(<Probe store={store} />);
    expect(start).toHaveBeenCalledTimes(1);
    expect(stop).not.toHaveBeenCalled();
    unmount();
    flushRelease();
    expect(stop).toHaveBeenCalledTimes(1);
  });

  it('switching to another store instance resubscribes', () => {
    vi.useFakeTimers();
    const a = countedWritable('a');
    const b = countedWritable('b');
    const { container, rerender } = render(<Probe store={a.store} />);
    expect(container.textContent).toBe('a');
    rerender(<Probe store={b.store} />);
    flushRelease();
    expect(a.stop).toHaveBeenCalledTimes(1);
    expect(b.start).toHaveBeenCalledTimes(1);
    expect(b.stop).not.toHaveBeenCalled();
    expect(container.textContent).toBe('b');
  });

  it('two components on one store share one subscription at a time and both update', () => {
    vi.useFakeTimers();
    const { store, start, stop } = countedWritable(0);
    const { container, unmount } = render(
      <>
        <Probe store={store} />
        <Probe store={store} />
      </>,
    );
    expect(start).toHaveBeenCalledTimes(1);
    act(() => store.set(5));
    expect(container.textContent).toBe('55');
    expect(start).toHaveBeenCalledTimes(1);
    unmount();
    flushRelease();
    expect(stop).toHaveBeenCalledTimes(1);
  });

  it('works with derived and with get-only readers', () => {
    const a = writable(1);
    const b = writable(10);
    const sum = derived([a, b], ([x, y]) => (x as number) + (y as number));
    const { container } = render(<Probe store={sum} />);
    expect(container.textContent).toBe('11');
    act(() => a.set(2));
    expect(container.textContent).toBe('12');

    const getOnly: Readable<string> = { subscribe: (run) => (run('static'), () => {}) };
    const second = render(<Probe store={getOnly} />);
    expect(second.container.textContent).toBe('static');
  });

  it('start runs exactly once per mounted consumer set', () => {
    vi.useFakeTimers();
    const { store, start, stop } = countedWritable(0);
    const { container, unmount } = render(
      <StrictMode>
        <Probe store={store} />
        <Probe store={store} />
      </StrictMode>,
    );
    expect(start).toHaveBeenCalledTimes(1);
    expect(stop).not.toHaveBeenCalled();
    act(() => store.set(5));
    expect(container.textContent).toBe('55');
    expect(start).toHaveBeenCalledTimes(1);
    expect(stop).not.toHaveBeenCalled();
    unmount();
    flushRelease();
    expect(stop).toHaveBeenCalledTimes(1);
  });

  it('never paints a stale value after an uncommitted render', async () => {
    vi.useFakeTimers();
    const store = writable(1);
    let resolve!: () => void;
    let ready = false;
    const pending = new Promise<void>((r) => {
      resolve = () => {
        ready = true;
        r();
      };
    });
    function Suspender() {
      if (!ready) throw pending;
      return null;
    }
    const committed: unknown[] = [];
    function Committing() {
      const value = useStore(store);
      useLayoutEffect(() => {
        committed.push(value);
      });
      return null;
    }
    render(
      <Suspense fallback={null}>
        <Committing />
        <Suspender />
      </Suspense>,
    );
    flushRelease(); // nothing committed yet
    act(() => store.set(2));
    await act(async () => {
      resolve();
      await pending;
    });
    expect(committed[0]).toBe(2);
  });
});
