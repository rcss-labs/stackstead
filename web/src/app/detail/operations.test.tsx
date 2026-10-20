import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { StrictMode } from 'react';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, onTestFinished, vi } from 'vitest';
import { get } from '$lib/store';
import type { Operation } from '$lib/api/types';
import { api } from '$lib/api/client';
import { changes } from '$lib/stores/live';
import { startRunningOps } from '$lib/stores/runningOps';
import { operationsRefresh, startStacks } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream } from '../../vitest-setup';
import { installHarness, mountDetail, operation, removeHarness, type Harness } from './detailTestKit';
import { OperationPanel } from './OperationPanel';
import { OperationsList } from './OperationsList';

const user = () => userEvent.setup();
const href = (id: string) => `/stacks/web?tab=operations&op=${id}`;
const finished: Operation = { id: 'a', stack: 'web', action: 'up', state: 'succeeded', startedAt: '2026-01-01T00:00:00Z', endedAt: '2026-01-01T00:01:05Z', exitCode: 0 };
const running: Operation = { id: 'b', stack: 'web', action: 'restart', state: 'running', startedAt: '2026-01-01T00:00:00Z' };

describe('OperationsList', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-01-01T00:00:10Z'));
  });
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });
  const renderList = (operations: Operation[], selected?: string) =>
    render(
      <MemoryRouter>
        <OperationsList operations={operations} selected={selected} href={href} />
      </MemoryRouter>,
    );

  it('rows show duration for finished ops', () => {
    renderList([finished]);
    expect(screen.getByText('1m 5s')).toBeTruthy();
    expect(screen.getByText('docker compose up')).toBeTruthy();
    expect(screen.getByRole('link').getAttribute('href')).toBe(href('a'));
  });

  it('selected row has aria-current', () => {
    renderList([finished, running], 'b');
    const links = screen.getAllByRole('link');
    expect(links[0].getAttribute('aria-current')).toBeNull();
    expect(links[1].getAttribute('aria-current')).toBe('true');
  });

  it('shows the empty state', () => {
    renderList([]);
    expect(screen.getByText('No operations yet.')).toBeTruthy();
  });

  it('running row ticks and stops ticking when finished', async () => {
    const view = renderList([running]);
    expect(screen.getByText('10s')).toBeTruthy();
    await act(() => vi.advanceTimersByTimeAsync(3000));
    expect(screen.getByText('13s')).toBeTruthy();

    view.rerender(
      <MemoryRouter>
        <OperationsList operations={[{ ...running, state: 'succeeded', endedAt: '2026-01-01T00:00:14Z' }]} href={href} />
      </MemoryRouter>,
    );
    expect(screen.getByText('14s')).toBeTruthy();
    // RelativeTime keeps its own 30 s timer; the 1 s ticker must be gone.
    await act(() => vi.advanceTimersByTimeAsync(5000));
    expect(screen.getByText('14s')).toBeTruthy();
    view.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('creates no ticker when nothing is running', () => {
    const view = renderList([finished]);
    // Only RelativeTime's own 30 s timer exists; a 1 s ticker would fire within this window.
    const timersBefore = vi.getTimerCount();
    expect(timersBefore).toBe(1);
    view.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('renders a host storage operation without a stack link or compose command', () => {
    renderList([{ ...finished, stack: '', action: 'storage-cleanup' }]);
    expect(screen.getByText('host')).toBeTruthy();
    expect(screen.queryByRole('link')).toBeNull();
    expect(screen.queryByText(/docker compose/)).toBeNull();
  });
});

describe('OperationPanel', () => {
  const PATH = '/api/operations/op1/stream';
  const line = (id: string, text: string) => `id: ${id}\nevent: line\ndata: ${JSON.stringify(text)}\n\n`;
  const op = (state: string) => ({ id: 'op1', stack: 'web', action: 'up', state, startedAt: '2026-01-01T00:00:00Z', exitCode: state === 'succeeded' ? 0 : undefined });
  const doneEv = (state = 'succeeded') => `event: done\ndata: ${JSON.stringify(op(state))}\n\n`;

  let stream: FakeStream;
  let fetcher: FakeFetch;
  afterEach(() => {
    cleanup();
    stream?.restore();
    fetcher?.restore();
  });

  function setup(chunks: string[], props: Partial<Parameters<typeof OperationPanel>[0]> = {}) {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: { ...op('running'), lines: [] } } });
    stream = installFakeStream({ [PATH]: chunks });
    return render(<OperationPanel operationId="op1" {...props} />);
  }

  it('renders lines as text in a log region with header details', async () => {
    setup([line('0', 'pulling <b>x</b>'), line('1', 'created')]);
    const log = await screen.findByRole('log', { name: 'Output' });
    await waitFor(() => expect(log.textContent).toContain('created'));
    expect(log.textContent).toContain('pulling <b>x</b>');
    expect(log.querySelector('b')).toBeNull();
    expect(log.getAttribute('aria-live')).toBe('off');
    expect(log.getAttribute('tabindex')).toBe('0');
    await waitFor(() => expect(screen.getByText('docker compose up')).toBeTruthy());
  });

  it('shows Cancel while running and calls onCancel with the id', async () => {
    const onCancel = vi.fn();
    setup([line('0', 'a')], { onCancel });
    await user().click(await screen.findByRole('button', { name: 'Cancel' }));
    expect(onCancel).toHaveBeenCalledWith('op1');
  });

  it('on done: calls onFinished, shows state and exit code, hides Cancel', async () => {
    const onFinished = vi.fn();
    setup([line('0', 'a'), doneEv()], { onFinished });
    await waitFor(() => expect(onFinished).toHaveBeenCalledTimes(1));
    expect(onFinished.mock.calls[0][0]).toMatchObject({ id: 'op1', state: 'succeeded' });
    expect(screen.getByText('succeeded', { selector: '[data-tone]' })).toBeTruthy();
    expect(screen.getByText(/exit 0/)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull();
  });

  it('shows the dropped note dimmed as the first line', async () => {
    setup([`event: line\ndata: ${JSON.stringify('... 3 earlier lines dropped')}\n\n`, line('3', 'a'), doneEv()]);
    const note = await screen.findByText('... 3 earlier lines dropped');
    expect(note.getAttribute('data-kind')).toBe('note');
  });

  it('announces and hides Cancel when the operation is no longer available', async () => {
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { status: 404, body: { error: 'nope' } } });
    stream = installFakeStream({ [PATH]: { responses: [{ status: 404, body: { error: 'nope' } }] } });
    render(<OperationPanel operationId="op1" />);
    expect((await screen.findAllByText('Operation no longer available')).length).toBeGreaterThan(0);
    expect(screen.getAllByRole('status').some((el) => el.textContent === 'Operation no longer available')).toBe(true);
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull();
  });

  it('Cancel ignores a double click while the request is pending', async () => {
    let release!: () => void;
    const onCancel = vi.fn(() => new Promise<void>((r) => (release = r)));
    setup([line('0', 'a')], { onCancel });
    const u = user();
    const btn = await screen.findByRole('button', { name: 'Cancel' });
    await u.click(btn);
    await u.click(btn);
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(btn.getAttribute('aria-busy')).toBe('true');
    await act(async () => release());
    await waitFor(() => expect(btn.getAttribute('aria-busy')).toBeNull());
  });

  it('unmounting aborts the stream', async () => {
    const view = setup([line('0', 'a')]);
    await screen.findByRole('log');
    await waitFor(() => expect(stream.requests).toHaveLength(1));
    view.unmount();
    expect(stream.requests[0].signal?.aborted).toBe(true);
  });

  it('shows an exit dash while running', async () => {
    setup([line('0', 'a')]);
    await waitFor(() => expect(screen.getByText(/exit —/)).toBeTruthy());
  });

  it('Copy output copies the joined lines', async () => {
    const u = user(); // setup() installs its own clipboard stub, so replace it afterwards
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    setup([line('0', 'one'), line('1', 'two')]);
    const log = await screen.findByRole('log');
    await waitFor(() => expect(log.textContent).toContain('two'));
    await u.click(screen.getByRole('button', { name: 'Copy output' }));
    expect(writeText).toHaveBeenCalledWith('one\ntwo');
  });

  it('marks success lines with a tone', async () => {
    setup([line('0', '✔ done'), line('1', 'plain')]);
    expect((await screen.findByText('✔ done')).getAttribute('data-tone')).toBe('success');
    expect(screen.getByText('plain').getAttribute('data-tone')).toBeNull();
  });

  it('Jump to latest appears when scrolled away', async () => {
    setup([line('0', 'a')]);
    const log = await screen.findByRole('log');
    await screen.findByText('a');
    expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull();
    Object.defineProperty(log, 'scrollHeight', { value: 1000, configurable: true });
    Object.defineProperty(log, 'clientHeight', { value: 100, configurable: true });
    Object.defineProperty(log, 'scrollTop', { value: 0, writable: true, configurable: true });
    fireEvent.scroll(log);
    const jump = await screen.findByRole('button', { name: 'Jump to latest' });
    await user().click(jump);
    expect(log.scrollTop).toBe(1000);
    expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull();
  });

  it('under StrictMode keeps one live stream, shows each line once and notifies once', async () => {
    const onFinished = vi.fn();
    fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: { ...op('running'), lines: [] } } });
    stream = installFakeStream({ [PATH]: [line('0', 'only')] });
    render(
      <StrictMode>
        <OperationPanel operationId="op1" onFinished={onFinished} />
      </StrictMode>,
    );
    expect(await screen.findByText('only')).toBeTruthy();
    // StrictMode's throwaway mount may send a request, but it must be closed: exactly one stays open.
    expect(stream.requests.filter((r) => !r.signal?.aborted)).toHaveLength(1);
    await act(async () => stream.push(PATH, doneEv()));
    await waitFor(() => expect(onFinished).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.getAllByText('only')).toHaveLength(1);
    expect(onFinished).toHaveBeenCalledTimes(1);
  });

  it('a new operationId without re-keying shows the new output and notifies for the new id only', async () => {
    const onFinished = vi.fn();
    const op2 = { ...op('succeeded'), id: 'op2', action: 'pull' };
    fetcher = installFakeFetch({
      'GET /api/operations/op1': { body: { ...op('running'), lines: [] } },
      'GET /api/operations/op2': { body: { ...op2, lines: [] } },
    });
    stream = installFakeStream({
      [PATH]: [line('0', 'first op'), doneEv()],
      '/api/operations/op2/stream': [line('0', 'second op'), `event: done\ndata: ${JSON.stringify(op2)}\n\n`],
    });
    const view = render(<OperationPanel operationId="op1" onFinished={onFinished} />);
    await waitFor(() => expect(onFinished).toHaveBeenCalledTimes(1));
    view.rerender(<OperationPanel operationId="op2" onFinished={onFinished} />);
    expect(await screen.findByText('second op')).toBeTruthy();
    await waitFor(() => expect(onFinished).toHaveBeenCalledTimes(2));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText('first op')).toBeNull();
    expect(onFinished.mock.calls.map(([o]) => o.id)).toEqual(['op1', 'op2']);
  });

  it('onFinished fires once for repeated done', async () => {
    const onFinished = vi.fn();
    const view = setup([line('0', 'a'), doneEv(), doneEv()], { onFinished });
    await waitFor(() => expect(onFinished).toHaveBeenCalledTimes(1));
    // A fresh callback identity after the finish must not notify again.
    view.rerender(<OperationPanel operationId="op1" onFinished={(o) => onFinished(o)} />);
    await new Promise((r) => setTimeout(r, 20));
    expect(onFinished).toHaveBeenCalledTimes(1);
  });
});

describe('operations pane on the stack page', () => {
  let h: Harness;
  beforeEach(() => {
    h = installHarness();
  });
  afterEach(async () => {
    cleanup();
    await removeHarness(h);
  });
  const doneEvent = (id: string, state = 'succeeded') =>
    `event: done\ndata: ${JSON.stringify({ id, stack: 'web', action: 'up', state, startedAt: '2026-10-14T10:00:00Z', exitCode: 0 })}\n\n`;
  const streamed = (path: string) => h.stream.requests.some((r) => r.path === path);

  it('?op= preselects an operation in the operations tab and lists recent ones', async () => {
    h.operations = [operation({ id: 'op1', state: 'succeeded', endedAt: '2026-10-14T10:01:00Z', exitCode: 0 }), operation({ id: 'op0', action: 'down', state: 'failed' })];
    mountDetail(h, '/stacks/web?tab=operations&op=op1');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await waitFor(() => expect(streamed('/api/operations/op1/stream')).toBe(true));
    expect(await screen.findByRole('heading', { level: 2, name: 'Operations' })).toBeTruthy();
    expect(await screen.findByRole('img', { name: 'failed' })).toBeTruthy();
    expect(screen.getByRole('region', { name: 'Operation output' })).toBeTruthy();
  });

  it('a done event for the selected operation refreshes stacks and running ops and reloads the detail', async () => {
    // The app shell owns these two stores; start them as it does so the refreshes are observable.
    const stopStacks = startStacks({ list: api.stacks, changes });
    const stopRunning = startRunningOps({ list: api.runningOperations, changes, refresh: operationsRefresh });
    onTestFinished(() => {
      stopStacks();
      stopRunning();
    });
    mountDetail(h, '/stacks/web?tab=operations&op=op1');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await waitFor(() => expect(streamed('/api/operations/op1/stream')).toBe(true));
    await waitFor(() => expect(h.calls('GET', '/api/stacks')).toBe(1));
    await waitFor(() => expect(h.calls('GET', '/api/operations?state=running')).toBe(1));
    const before = {
      stacks: h.calls('GET', '/api/stacks'),
      running: h.calls('GET', '/api/operations?state=running'),
      detail: h.calls('GET', '/api/stacks/web'),
    };
    await act(async () => h.stream.push('/api/operations/op1/stream', doneEvent('op1')));
    await waitFor(() => expect(h.calls('GET', '/api/stacks/web')).toBeGreaterThan(before.detail));
    await waitFor(() => expect(h.calls('GET', '/api/stacks')).toBeGreaterThan(before.stacks));
    await waitFor(() => expect(h.calls('GET', '/api/operations?state=running')).toBeGreaterThan(before.running));
  });

  it('Cancel posts once to the cancel endpoint and toasts on failure', async () => {
    mountDetail(h, '/stacks/web?tab=operations&op=op1');
    const cancel = await screen.findByRole('button', { name: 'Cancel' });
    await user().click(cancel);
    await waitFor(() => expect(h.calls('POST', '/api/operations/op1/cancel')).toBe(1));
    await waitFor(() => expect(cancel.getAttribute('aria-busy')).toBeNull());
    expect(get(toasts).filter((t) => t.kind === 'danger')).toEqual([]);

    h.cancelRes = { status: 500, body: { error: 'boom' } };
    await user().click(cancel);
    await waitFor(() => expect(get(toasts).some((t) => t.kind === 'danger' && /^Could not cancel: .*boom/.test(t.message))).toBe(true));
    expect(h.calls('POST', '/api/operations/op1/cancel')).toBe(2);
  });

  it('the rail Operations count follows the list', async () => {
    mountDetail(h, '/stacks/web?tab=operations&op=op1');
    const railButton = async () => within(await screen.findByRole('navigation', { name: 'Services' })).getByRole('button', { name: /^Operations/ });
    await waitFor(async () => expect((await railButton()).textContent).toBe('Operations 2'));
    h.operations = [...h.operations, operation({ id: 'op3', action: 'down', state: 'succeeded' })];
    await act(async () => h.stream.push('/api/operations/op1/stream', doneEvent('op1')));
    await waitFor(async () => expect((await railButton()).textContent).toBe('Operations 3'));
    expect(screen.getAllByRole('link').filter((a) => a.getAttribute('href')?.includes('&op='))).toHaveLength(3);
  });
});
