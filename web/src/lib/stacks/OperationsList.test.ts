import { render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Operation } from '$lib/api/types';
import OperationsList from './OperationsList.svelte';

const href = (id: string) => `/stacks/web?tab=operations&op=${id}`;
const finished: Operation = { id: 'a', stack: 'web', action: 'up', state: 'succeeded', startedAt: '2026-01-01T00:00:00Z', endedAt: '2026-01-01T00:01:05Z', exitCode: 0 };
const running: Operation = { id: 'b', stack: 'web', action: 'restart', state: 'running', startedAt: '2026-01-01T00:00:00Z' };

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date('2026-01-01T00:00:10Z'));
});
afterEach(() => vi.useRealTimers());

describe('OperationsList', () => {
  it('rows show duration for finished ops', () => {
    render(OperationsList, { operations: [finished], href });
    expect(screen.getByText('1m 5s')).toBeTruthy();
    expect(screen.getByText('docker compose up')).toBeTruthy();
    expect(screen.getByRole('link').getAttribute('href')).toBe(href('a'));
  });

  it('selected row has aria-current', () => {
    render(OperationsList, { operations: [finished, running], selected: 'b', href });
    const links = screen.getAllByRole('link');
    expect(links[0].getAttribute('aria-current')).toBeNull();
    expect(links[1].getAttribute('aria-current')).toBe('true');
  });

  it('shows the empty state', () => {
    render(OperationsList, { operations: [], href });
    expect(screen.getByText('No operations yet.')).toBeTruthy();
  });

  it('running row ticks and stops ticking when finished', async () => {
    const { rerender, unmount } = render(OperationsList, { operations: [running], href });
    expect(screen.getByText('10s')).toBeTruthy();
    await vi.advanceTimersByTimeAsync(3000);
    expect(screen.getByText('13s')).toBeTruthy();
    expect(vi.getTimerCount()).toBeGreaterThan(0);

    await rerender({ operations: [{ ...running, state: 'succeeded', endedAt: '2026-01-01T00:00:14Z' }], href });
    const afterFinish = screen.getByText('14s');
    expect(afterFinish).toBeTruthy();
    // RelativeTime keeps its own 30 s timer; the 1 s ticker must be gone.
    await vi.advanceTimersByTimeAsync(5000);
    expect(screen.getByText('14s')).toBeTruthy();
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('creates no ticker when nothing is running', () => {
    const { unmount } = render(OperationsList, { operations: [finished], href });
    const before = vi.getTimerCount();
    unmount();
    expect(vi.getTimerCount()).toBeLessThan(before + 1);
    expect(vi.getTimerCount()).toBe(0);
  });
});
