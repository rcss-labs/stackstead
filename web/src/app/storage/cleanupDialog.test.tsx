import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { StrictMode } from 'react';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import type { StoragePlan } from '$lib/api/types';
import { operationsRefresh } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream } from '../../vitest-setup';
import { CleanupDialog } from './CleanupDialog';

let fake: FakeFetch;
let stream: FakeStream | undefined;
let plan: StoragePlan;
let response: FakeResponse | undefined;
let confirm: FakeResponse;
const op = { id: 'op1', stack: '', action: 'storage-cleanup', state: 'running', startedAt: '2026-01-01T00:00:00Z' };

beforeEach(() => {
  toasts.reset();
  response = undefined;
  plan = {
    id: 'p_1',
    kind: 'volumes',
    createdAt: new Date().toISOString(),
    expiresAt: new Date(Date.now() + 300000).toISOString(),
    items: [{ kind: 'volume', id: 'free', ref: 'free', bytes: 1024 }],
    protected: [{ kind: 'volume', id: 'db', ref: 'db', reason: 'declared by stack', stack: 'media', stackState: 'stopped' }],
    warnings: ['Check backups'],
    reclaim: { min: 1024, max: 1024, exact: true },
    confirmPhrase: 'delete 1 volume',
  };
  confirm = { body: op };
  fake = installFakeFetch({
    'POST /api/storage/plans': () => response ?? { body: plan },
    'POST /api/storage/plans/p_1/confirm': () => confirm,
    'GET /api/operations/op1': { body: { ...op, lines: [] } },
  });
});

afterEach(() => {
  cleanup();
  stream?.restore();
  stream = undefined;
  fake.restore();
  toasts.reset();
  vi.useRealTimers();
});

function open(onClose = vi.fn(), onFinished = vi.fn(), strict = false) {
  const dialog = (
    <MemoryRouter>
      <CleanupDialog request={{ kind: 'volumes', selection: 'unused' }} onClose={onClose} onFinished={onFinished} />
    </MemoryRouter>
  );
  return render(strict ? <StrictMode>{dialog}</StrictMode> : dialog);
}
const type = (input: HTMLElement, value: string) => fireEvent.change(input, { target: { value } });
const planCalls = () => fake.calls.filter((c) => c.path === '/api/storage/plans');

describe('CleanupDialog', () => {
  it('previews bytes, protections, warnings and volume data warning without selectable protected entries', async () => {
    open();
    const dialog = await screen.findByRole('dialog');
    await screen.findByText('Frees 1.0 KiB');
    expect(within(dialog).getByText('free')).toBeTruthy();
    expect(within(dialog).getByText('1.0 KiB')).toBeTruthy();
    expect(within(dialog).getByText(/declared by stack/)).toBeTruthy();
    expect(screen.getByRole('link', { name: 'media' }).getAttribute('href')).toBe('/stacks/media');
    expect(screen.queryByRole('checkbox')).toBeNull();
    expect(screen.getByText('Check backups')).toBeTruthy();
    expect(screen.getByText('Volumes hold data. This cannot be undone.')).toBeTruthy();
    expect(screen.getByText(/Expires in 5:00/)).toBeTruthy();
  });

  it('focuses the described field and gates Delete on exact case and whitespace', async () => {
    open();
    const input = await screen.findByRole('textbox', { name: 'Type delete 1 volume to confirm' });
    await waitFor(() => expect(document.activeElement).toBe(input));
    expect(document.getElementById(input.getAttribute('aria-describedby')!)?.textContent).toContain(plan.confirmPhrase);
    const button = screen.getByRole('button', { name: 'Delete' });
    expect(button.hasAttribute('disabled')).toBe(true);
    for (const value of ['delete 1 volume ', 'Delete 1 volume', ' delete 1 volume']) {
      type(input, value);
      expect(button.hasAttribute('disabled')).toBe(true);
    }
    type(input, plan.confirmPhrase);
    expect(button.hasAttribute('disabled')).toBe(false);
  });

  it('Escape closes before confirmation', async () => {
    const close = vi.fn();
    open(close);
    await screen.findByRole('textbox');
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(close).toHaveBeenCalledTimes(1);
    expect(fake.calls.some((c) => c.path.endsWith('/confirm'))).toBe(false);
  });

  it.each([409, 503])('preserves server %s plan error with Close', async (status) => {
    response = { status, body: { error: 'Storage is busy; try later' } };
    open();
    expect(await screen.findByText('Storage is busy; try later')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Close' })).toBeTruthy();
    expect(screen.queryByRole('textbox')).toBeNull();
  });

  it('shows nothing to clean for an empty plan', async () => {
    plan.items = [];
    open();
    expect(await screen.findByText('Nothing to clean')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull();
  });

  it('keeps focus inside the dialog on Close when the check ends without a confirm field', async () => {
    response = { status: 409, body: { error: 'Storage is busy; try later' } };
    const failed = open();
    const dialog = await screen.findByRole('dialog');
    await screen.findByText('Storage is busy; try later');
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Close' }));
    failed.unmount();
    response = undefined;
    plan.items = [];
    plan.protected = [];
    open();
    const empty = await screen.findByRole('dialog');
    await screen.findByText('Nothing to clean');
    await waitFor(() => expect(empty.contains(document.activeElement)).toBe(true));
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Close' }));
  });

  // jsdom cannot measure layout, so this asserts the data attribute the stylesheet turns into a lower max-height.
  it('gives the items and protected lists a shared reduced cap only when both are shown', async () => {
    const both = open();
    await screen.findByRole('textbox');
    const regions = screen.getAllByRole('region');
    expect(regions).toHaveLength(2);
    for (const region of regions) expect(region.getAttribute('data-shared')).toBe('true');
    both.unmount();
    plan.protected = [];
    open();
    await screen.findByRole('textbox');
    expect(screen.getByRole('region').hasAttribute('data-shared')).toBe(false);
  });

  it('labels fallback not exact beside its phrase', async () => {
    plan.kind = 'build-cache';
    plan.mode = 'all-unused';
    plan.reclaim.exact = false;
    open();
    const phrase = await screen.findByText(plan.confirmPhrase, { selector: 'code' });
    expect(phrase.parentElement?.textContent).toContain('not exact');
    expect(screen.getByText('Frees about 1.0 KiB (not exact)')).toBeTruthy();
  });

  it('expires at zero and re-checks with a new plan and empty field', async () => {
    vi.useFakeTimers();
    plan.expiresAt = new Date(Date.now() + 1000).toISOString();
    open();
    await act(() => vi.advanceTimersByTimeAsync(0));
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull();
    plan = { ...plan, id: 'p_2', expiresAt: new Date(Date.now() + 300000).toISOString() };
    fireEvent.click(screen.getByRole('button', { name: 'Re-check' }));
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect((screen.getByRole('textbox') as HTMLInputElement).value).toBe('');
    expect(screen.getByRole('button', { name: 'Delete' }).hasAttribute('disabled')).toBe(true);
    expect(planCalls()).toHaveLength(2);
  });

  it.each([400, 404])('handles confirm %s as field error or expired plan', async (status) => {
    confirm = { status, body: { error: status === 400 ? 'Wrong phrase' : 'Plan expired', fields: [{ field: 'confirm', message: 'Phrase must match exactly' }] } };
    open();
    const input = await screen.findByRole('textbox');
    type(input, plan.confirmPhrase);
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    if (status === 400) {
      expect(await screen.findByText('Phrase must match exactly')).toBeTruthy();
      expect(input.getAttribute('aria-invalid')).toBe('true');
    } else {
      expect(await screen.findByRole('button', { name: 'Re-check' })).toBeTruthy();
    }
  });

  it('streams output then emits toast, refresh signal and completion once', async () => {
    stream = installFakeStream({ '/api/operations/op1/stream': [] });
    const finished = vi.fn();
    const before = get(operationsRefresh);
    open(vi.fn(), finished);
    const input = await screen.findByRole('textbox');
    type(input, plan.confirmPhrase);
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    await screen.findByRole('log');
    await waitFor(() => expect(stream!.requests).toHaveLength(1));
    stream.push('/api/operations/op1/stream', 'event: line\ndata: "deleted volume free"\n\n');
    expect(await screen.findByText('deleted volume free')).toBeTruthy();
    expect(finished).not.toHaveBeenCalled();
    stream.push('/api/operations/op1/stream', `event: done\ndata: ${JSON.stringify({ ...op, state: 'succeeded', exitCode: 0 })}\n\n`);
    await waitFor(() => expect(finished).toHaveBeenCalledTimes(1));
    expect(get(operationsRefresh)).toBe(before + 1);
    expect(get(toasts)[0].message).toContain('succeeded');
    expect(screen.queryByText('docker compose storage-cleanup')).toBeNull();
  });

  it('shows checking state until the fresh plan resolves', async () => {
    let release!: (value: FakeResponse) => void;
    fake.restore();
    fake = installFakeFetch({ 'POST /api/storage/plans': () => new Promise<FakeResponse>((resolve) => (release = resolve)) });
    open();
    expect(await screen.findByText('Checking current state…')).toBeTruthy();
    expect(screen.queryByRole('textbox')).toBeNull();
    release({ body: plan });
    expect(await screen.findByRole('textbox')).toBeTruthy();
    expect(screen.queryByText('Checking current state…')).toBeNull();
  });

  it('keeps long plans reachable in named scroll regions with counts, ahead of the confirm controls', async () => {
    plan.items = Array.from({ length: 99 }, (_, i) => ({ kind: 'volume', id: `v${i}`, ref: `volume-${i}`, bytes: 1024 }));
    open();
    const items = await screen.findByRole('region', { name: 'Items to delete (99)' });
    expect(items.getAttribute('tabindex')).toBe('0');
    expect(within(items).getAllByRole('listitem')).toHaveLength(99);
    expect(within(items).getByText('volume-98')).toBeTruthy();
    const protectedList = screen.getByRole('region', { name: 'Protected (1)' });
    expect(protectedList.getAttribute('tabindex')).toBe('0');
    const input = screen.getByRole('textbox');
    expect(items.compareDocumentPosition(input) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('focuses the confirm field without scrolling the dialog', async () => {
    const focus = vi.spyOn(HTMLElement.prototype, 'focus');
    try {
      open();
      const input = await screen.findByRole('textbox');
      await waitFor(() => expect(document.activeElement).toBe(input));
      expect(focus.mock.contexts.some((c, i) => c === input && (focus.mock.calls[i][0] as FocusOptions | undefined)?.preventScroll === true)).toBe(true);
    } finally {
      focus.mockRestore();
    }
  });

  it('separates not exact from the phrase in text', async () => {
    plan.kind = 'build-cache';
    plan.mode = 'all-unused';
    open();
    const phrase = await screen.findByText(plan.confirmPhrase, { selector: 'code' });
    expect(phrase.parentElement?.textContent).toBe(`${plan.confirmPhrase} not exact`);
  });

  it('words protected items as reason · stack (state) with a human state', async () => {
    plan.protected = [{ ...plan.protected[0], stackState: 'not-deployed' }];
    open();
    const region = await screen.findByRole('region', { name: 'Protected (1)' });
    expect(within(region).getByRole('listitem').textContent).toBe('dbdeclared by stack · media (not deployed)');
  });

  it('sends the execute request once for two clicks dispatched in the same task', async () => {
    open();
    const input = await screen.findByRole('textbox');
    type(input, plan.confirmPhrase);
    const button = screen.getByRole('button', { name: 'Delete' });
    // One act batches both dispatches, so React cannot re-render (and disable the button) in between.
    act(() => {
      button.click();
      button.click();
    });
    await waitFor(() => expect(fake.calls.filter((c) => c.path.endsWith('/confirm'))).toHaveLength(1));
    await act(() => Promise.resolve());
    expect(fake.calls.filter((c) => c.path.endsWith('/confirm'))).toHaveLength(1);
  });

  it('the plan is created once per mount under StrictMode', async () => {
    open(vi.fn(), vi.fn(), true);
    expect(await screen.findByRole('textbox')).toBeTruthy();
    expect(screen.queryByText('Checking current state…')).toBeNull();
    expect(planCalls()).toHaveLength(1);
  });
});
