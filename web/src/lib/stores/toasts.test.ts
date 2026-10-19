import { get } from '$lib/store';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { toasts } from './toasts';

describe('toasts store', () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => {
    toasts.reset();
    vi.useRealTimers();
  });

  it('auto-dismisses after the timeout', () => {
    toasts.push({ kind: 'success', message: 'Saved', timeoutMs: 1000 });
    expect(get(toasts).map((t) => t.message)).toEqual(['Saved']);
    vi.advanceTimersByTime(999);
    expect(get(toasts)).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(get(toasts)).toHaveLength(0);
  });

  it('keeps a toast with timeoutMs 0 until dismissed', () => {
    const id = toasts.push({ kind: 'danger', message: 'Boom', timeoutMs: 0 });
    vi.advanceTimersByTime(600_000);
    expect(get(toasts)).toHaveLength(1);
    toasts.dismiss(id);
    expect(get(toasts)).toHaveLength(0);
  });

  it('gives each toast a distinct id and dismisses only that one', () => {
    const a = toasts.push({ kind: 'info', message: 'a', timeoutMs: 0 });
    const b = toasts.push({ kind: 'info', message: 'b', timeoutMs: 0 });
    expect(a).not.toBe(b);
    toasts.dismiss(a);
    expect(get(toasts).map((t) => t.id)).toEqual([b]);
  });

  it('keeps danger toasts until dismissed by default', () => {
    toasts.push({ kind: 'danger', message: 'Failed' });
    vi.advanceTimersByTime(600_000);
    expect(get(toasts)).toHaveLength(1);
  });

  it('uses a default timeout and ignores dismissing an unknown id', () => {
    toasts.push({ kind: 'info', message: 'x' });
    toasts.dismiss(9999);
    expect(get(toasts)).toHaveLength(1);
    vi.advanceTimersByTime(30_000);
    expect(get(toasts)).toHaveLength(0);
  });

  it('pending toasts do not expire until updated', () => {
    const id = toasts.push({ kind: 'info', message: 'Working', pending: true });
    vi.advanceTimersByTime(600_000);
    expect(get(toasts)).toHaveLength(1);
    expect(get(toasts)[0].pending).toBe(true);
    toasts.update(id, { kind: 'success', message: 'Done', pending: false });
    vi.advanceTimersByTime(5000);
    expect(get(toasts)).toHaveLength(0);
  });

  it('update re-arms the timer and swaps the message, unknown id is ignored', () => {
    const id = toasts.push({ kind: 'info', message: 'a', timeoutMs: 1000 });
    vi.advanceTimersByTime(900);
    toasts.update(id, { message: 'b', timeoutMs: 1000 });
    expect(get(toasts)[0].message).toBe('b');
    vi.advanceTimersByTime(900);
    expect(get(toasts)).toHaveLength(1);
    vi.advanceTimersByTime(100);
    expect(get(toasts)).toHaveLength(0);
    toasts.update(9999, { message: 'zzz' });
    expect(get(toasts)).toHaveLength(0);
  });

  it('link is carried through', () => {
    toasts.push({ kind: 'info', message: 'x', timeoutMs: 0, link: { label: 'View', href: '/a' } });
    expect(get(toasts)[0].link).toEqual({ label: 'View', href: '/a' });
  });
});
