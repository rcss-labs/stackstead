import { StrictMode } from 'react';
import { act, render } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { derived, writable } from '$lib/store';
import { useStore } from './useStore';
import type { Readable } from '$lib/store';

function Probe({ store, onRender }: { store: Readable<unknown>; onRender?: () => void }) {
  const value = useStore(store);
  onRender?.();
  return <span data-testid="v">{String(value)}</span>;
}

// A writable whose start/stop notifier counts live subscriptions. The hook's
// one-off first-render read opens and closes a transient subscription, so
// tests assert on `live()` (starts minus stops), not raw call counts.
function countedWritable<T>(initial: T) {
  const stop = vi.fn();
  const start = vi.fn(() => stop);
  return { store: writable(initial, start), start, stop, live: () => start.mock.calls.length - stop.mock.calls.length };
}

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
    const { store, live } = countedWritable(0);
    const { unmount } = render(<Probe store={store} />);
    expect(live()).toBe(1);
    unmount();
    expect(live()).toBe(0);
  });

  it('switching to another store instance resubscribes', () => {
    const a = countedWritable('a');
    const b = countedWritable('b');
    const { container, rerender } = render(<Probe store={a.store} />);
    expect(container.textContent).toBe('a');
    rerender(<Probe store={b.store} />);
    expect(a.live()).toBe(0);
    expect(b.live()).toBe(1);
    expect(container.textContent).toBe('b');
  });

  it('two components on one store share one subscription at a time and both update', () => {
    const { store, live } = countedWritable(0);
    const { container, unmount } = render(
      <>
        <Probe store={store} />
        <Probe store={store} />
      </>,
    );
    expect(live()).toBe(1);
    act(() => store.set(5));
    expect(container.textContent).toBe('55');
    expect(live()).toBe(1);
    unmount();
    expect(live()).toBe(0);
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

  it('is safe under StrictMode double-invocation', () => {
    const { store, live } = countedWritable(0);
    const { container, unmount } = render(
      <StrictMode>
        <Probe store={store} />
      </StrictMode>,
    );
    act(() => store.set(3));
    expect(container.textContent).toBe('3');
    expect(live()).toBe(1);
    unmount();
    expect(live()).toBe(0);
  });
});
