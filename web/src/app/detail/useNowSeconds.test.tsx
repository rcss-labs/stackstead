import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useNowSeconds } from './useNowSeconds';

function setVisibility(state: 'visible' | 'hidden') {
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
  document.dispatchEvent(new Event('visibilitychange'));
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(1_000_000_000));
});
afterEach(() => {
  setVisibility('visible');
  vi.useRealTimers();
});

describe('useNowSeconds', () => {
  it('ticks while visible, stops while hidden and refreshes at once when visible again', () => {
    const { result } = renderHook(() => useNowSeconds(60_000));
    expect(result.current).toBe(1_000_000);
    act(() => void vi.advanceTimersByTime(60_000));
    expect(result.current).toBe(1_000_060);

    act(() => setVisibility('hidden'));
    expect(vi.getTimerCount()).toBe(0);
    act(() => void vi.advanceTimersByTime(600_000));
    expect(result.current).toBe(1_000_060);

    act(() => setVisibility('visible'));
    expect(result.current).toBe(1_000_660);
    expect(vi.getTimerCount()).toBe(1);
  });

  it('leaves no timers after unmount', () => {
    const { unmount } = renderHook(() => useNowSeconds(60_000));
    expect(vi.getTimerCount()).toBe(1);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
