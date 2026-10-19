import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installFakeStream, type FakeStream } from '../../vitest-setup';
import type { LogSource } from '$lib/stacks/logs';
import { LogConsole } from './LogConsole';

const PATH = '/api/stacks/web/logs';
const C_A = 'aaaaaaaaaaaa';
const C_B = 'bbbbbbbbbbbb';
let fake: FakeStream;
const tick = (ms = 50) => act(() => vi.advanceTimersByTimeAsync(ms));

function logEvent(container: string, ns: number, ord: number, text: string, service = 'api', stream = 'stdout') {
  const data = JSON.stringify({ service, container, stream, time: '2026-10-14T10:00:00Z', text });
  return `id: ${container}:${ns}:${ord}\nevent: log\ndata: ${data}\n\n`;
}

const storage = { getItem: () => null, setItem: () => undefined } as unknown as Storage;
const rows = () => screen.queryAllByTestId('log-row');
const scroller = () => screen.getByRole('log');

// The console takes its service filter from the page; this stands in for it.
function Harness({ source, services }: { source: LogSource; services: string[] }) {
  const [service, setService] = useState<string | undefined>(undefined);
  return <LogConsole source={source} services={services} service={service} onService={setService} open onOpenChange={() => undefined} mobile={false} storage={storage} />;
}

function mount(source: LogSource = { kind: 'stack', name: 'web' }, services = ['api', 'worker']) {
  return render(<Harness source={source} services={services} />);
}

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  cleanup();
  fake?.restore();
  vi.useRealTimers();
});

describe('LogConsole lines', () => {
  it('TestFloodRendersBoundedRows', async () => {
    const flood = Array.from({ length: 20000 }, (_, i) => logEvent(C_A, i + 1, 1, `line ${i}`)).join('');
    fake = installFakeStream({ [PATH]: [flood] });
    mount();
    await tick(200);
    expect(rows().length).toBeGreaterThan(0);
    expect(rows().length).toBeLessThanOrEqual(5000);
    expect(screen.getByText(/15[, ]?000 older lines dropped/)).toBeTruthy();
  });

  it('renders text as text and filters by search', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, '<script>alert(1)</script> boom'), logEvent(C_A, 2, 1, 'quiet')] });
    const { container } = mount();
    await tick();
    expect(screen.getByText('<script>alert(1)</script> boom')).toBeTruthy();
    expect(container.querySelector('script')).toBeNull();
    fireEvent.change(screen.getByLabelText('Search logs'), { target: { value: 'BOOM' } });
    await tick(200);
    expect(rows()).toHaveLength(1);
    expect(container.querySelector('mark')!.textContent).toBe('boom');
  });

  it('levels filter by lineTone', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'ok'), logEvent(C_A, 2, 1, 'WARN w'), logEvent(C_A, 3, 1, 'ERROR e')] });
    mount();
    await tick();
    expect(rows().map((r) => r.getAttribute('data-tone'))).toEqual([null, 'warning', 'danger']);
    fireEvent.click(screen.getByRole('button', { name: 'Warn+' }));
    expect(rows()).toHaveLength(2);
    fireEvent.click(screen.getByRole('button', { name: 'Errors' }));
    expect(rows()).toHaveLength(1);
    expect(rows()[0].getAttribute('data-tone')).toBe('danger');
  });

  it('No lines match only when filters hide a non-empty buffer', async () => {
    fake = installFakeStream({ [PATH]: [] });
    mount();
    await tick();
    expect(screen.queryByText('No lines match.')).toBeNull();
    cleanup();
    fake.restore();
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'fine')] });
    mount();
    await tick();
    fireEvent.click(screen.getByRole('button', { name: 'Errors' }));
    expect(rows()).toHaveLength(0);
    expect(screen.getByText('No lines match.')).toBeTruthy();
  });

  it('service chips restart the stream with the new query and show only its lines', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'x')] });
    mount();
    await tick();
    fireEvent.click(within(screen.getByLabelText('Service filter')).getByRole('button', { name: 'worker' }));
    await tick();
    expect(fake.requests.at(-1)!.path).toContain('service=worker');
    expect(fake.requests.at(-1)!.headers.get('Last-Event-ID')).toBeNull();
    expect(rows()).toHaveLength(1);
    expect(within(screen.getByLabelText('Service filter')).getByRole('button', { name: 'worker' }).getAttribute('aria-pressed')).toBe('true');
  });

  it('the All chip clears the filter', async () => {
    fake = installFakeStream({ [PATH]: [] });
    mount();
    await tick();
    const chips = within(screen.getByLabelText('Service filter'));
    fireEvent.click(chips.getByRole('button', { name: 'worker' }));
    await tick();
    fireEvent.click(chips.getByRole('button', { name: 'All services' }));
    await tick();
    expect(fake.requests.at(-1)!.path).not.toContain('service=');
    expect(chips.getByRole('button', { name: 'All services' }).getAttribute('aria-pressed')).toBe('true');
  });

  it('a reconnect resumes from the composite cursor without repeating lines', async () => {
    fake = installFakeStream({
      [PATH]: { responses: [{ chunks: [logEvent(C_A, 10, 1, 'from a'), logEvent(C_B, 11, 1, 'from b')] }, { chunks: [logEvent(C_A, 12, 1, 'late')] }] },
    });
    mount();
    await tick();
    expect(rows()).toHaveLength(2);
    await act(async () => {
      fake.close(PATH);
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(fake.requests).toHaveLength(2);
    expect((fake.requests[1].headers.get('Last-Event-ID') ?? '').split(',').sort()).toEqual([`${C_A}:10:1`, `${C_B}:11:1`]);
    expect(rows()).toHaveLength(3);
    expect(screen.getAllByText('late')).toHaveLength(1);
  });

  it('shows per-container errors, dismissible', async () => {
    const err = JSON.stringify({ service: 'api', container: C_A, error: 'gone <b>x</b>' });
    fake = installFakeStream({ [PATH]: [`event: error\ndata: ${err}\n\n`] });
    mount();
    await tick();
    const alert = screen.getByRole('alert');
    expect(alert.textContent).toContain('gone <b>x</b>');
    expect(alert.textContent).toContain(`api (${C_A})`);
    fireEvent.click(within(alert).getByRole('button', { name: /dismiss/i }));
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('labels dismiss buttons and keeps the log region quiet for screen readers', async () => {
    const err = JSON.stringify({ service: 'api', container: 'web-api-1', error: 'oops' });
    fake = installFakeStream({ [PATH]: [`event: error\ndata: ${err}\n\n`] });
    mount();
    await tick();
    expect(screen.getByRole('button', { name: 'Dismiss error for web-api-1' })).toBeTruthy();
    expect(scroller().getAttribute('aria-live')).toBe('off');
    expect(scroller().getAttribute('aria-label')).toBe('Log output');
    expect(scroller().getAttribute('tabindex')).toBe('0');
  });

  it.each([
    [{ kind: 'stack', name: 'web' } as const, '/api/stacks/web/logs', 'stack or logs no longer available'],
    [{ kind: 'container', id: 'abc123def456' } as const, '/api/containers/abc123def456/logs', 'container or logs no longer available'],
  ])('words the gone status by source kind (%#)', async (source, path, text) => {
    fake = installFakeStream({ [path]: { responses: [{ status: 404, body: { error: 'nope' } }] } });
    mount(source, []);
    await tick();
    expect(screen.getByText(text)).toBeTruthy();
  });

  it('shows end of log when the stream ends', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'x'), 'event: end\ndata: {}\n\n'] });
    mount();
    await tick();
    expect(screen.getByText(/end of log/i)).toBeTruthy();
  });

  it('offers Reconnect after the stream ends and Clear resets the new-lines count', async () => {
    fake = installFakeStream({ [PATH]: { responses: [{ chunks: [logEvent(C_A, 1, 1, 'a'), 'event: end\ndata: {}\n\n'] }, { chunks: [] }] } });
    mount();
    await tick();
    fireEvent.click(screen.getByRole('button', { name: 'Reconnect' }));
    await tick();
    expect(fake.requests).toHaveLength(2);
    expect(screen.queryByRole('button', { name: 'Reconnect' })).toBeNull();
    expect(rows()).toHaveLength(0);
  });

  it('Clear log empties the console and the new-lines count', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'a')] });
    mount();
    await tick();
    fireEvent.click(screen.getByRole('button', { name: 'Following' }));
    await act(async () => {
      fake.push(PATH, logEvent(C_A, 2, 1, 'b'));
      await vi.advanceTimersByTimeAsync(200);
    });
    expect(screen.getByRole('button', { name: 'Paused · 1 new' })).toBeTruthy();
    fireEvent.click(screen.getByRole('button', { name: 'Clear log' }));
    expect(rows()).toHaveLength(0);
    expect(screen.getByRole('button', { name: 'Paused' })).toBeTruthy();
  });

  it('pause freezes the view and counts new lines in the pill', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'first')] });
    mount();
    await tick();
    fireEvent.click(screen.getByRole('button', { name: 'Following' }));
    await act(async () => {
      fake.push(PATH, logEvent(C_A, 2, 1, 'second') + logEvent(C_A, 3, 1, 'third'));
      await vi.advanceTimersByTimeAsync(200);
    });
    expect(rows()).toHaveLength(1);
    const pill = screen.getByRole('button', { name: 'Paused · 2 new' });
    Object.defineProperty(scroller(), 'scrollHeight', { configurable: true, value: 900 });
    Object.defineProperty(scroller(), 'clientHeight', { configurable: true, value: 200 });
    fireEvent.click(pill);
    expect(rows()).toHaveLength(3);
    expect(scroller().scrollTop).toBe(900);
  });

  it('Jump to latest resumes a paused console', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'first')] });
    mount();
    await tick();
    fireEvent.click(screen.getByRole('button', { name: 'Following' }));
    await act(async () => {
      fake.push(PATH, logEvent(C_A, 2, 1, 'second'));
      await vi.advanceTimersByTimeAsync(200);
    });
    fireEvent.click(screen.getByRole('button', { name: 'Jump to latest' }));
    expect(rows()).toHaveLength(2);
    expect(screen.getByRole('button', { name: 'Following' })).toBeTruthy();
  });

  it('chip change while paused unpauses', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'a')] });
    mount();
    await tick();
    fireEvent.click(screen.getByRole('button', { name: 'Following' }));
    fireEvent.click(within(screen.getByLabelText('Service filter')).getByRole('button', { name: 'worker' }));
    await tick();
    expect(screen.getByRole('button', { name: 'Following' })).toBeTruthy();
  });

  it('while paused, a session-expired stream still announces it', async () => {
    fake = installFakeStream({ [PATH]: { responses: [{ chunks: [logEvent(C_A, 1, 1, 'a')] }, { status: 401, body: {} }] } });
    mount();
    await tick();
    fireEvent.click(screen.getByRole('button', { name: 'Following' }));
    await act(async () => {
      fake.close(PATH);
      await vi.advanceTimersByTimeAsync(60000);
    });
    expect(screen.getByRole('status').textContent).toBe('session expired - sign in again');
  });

  it('while paused, a reconnecting stream reads reconnecting in the warning tone', async () => {
    fake = installFakeStream({ [PATH]: { responses: [{ chunks: [logEvent(C_A, 1, 1, 'a')] }, { chunks: [] }] } });
    mount();
    await tick();
    fireEvent.click(screen.getByRole('button', { name: 'Following' }));
    await act(async () => {
      fake.close(PATH);
      await vi.advanceTimersByTimeAsync(1);
    });
    expect(screen.getByRole('status').textContent).toBe('reconnecting');
    expect(screen.getByRole('status').getAttribute('data-tone')).toBe('warning');
  });

  it('container source hides the chips and opens the container URL', async () => {
    fake = installFakeStream({ '/api/containers/abc123def456/logs': [logEvent(C_A, 1, 1, 'x')] });
    mount({ kind: 'container', id: 'abc123def456' }, []);
    await tick();
    expect(screen.queryByLabelText('Service filter')).toBeNull();
    expect(fake.requests[0].path).toContain('/api/containers/abc123def456/logs');
    expect(fake.requests[0].path).not.toContain('service=');
    expect(rows()).toHaveLength(1);
  });

  it('clicking a line pins it and clicking again unpins; Clear drops the pin', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'one'), logEvent(C_A, 2, 1, 'two')] });
    mount();
    await tick();
    fireEvent.click(rows()[0]);
    expect(rows()[0].getAttribute('data-pinned')).toBe('true');
    expect(rows()[0].getAttribute('aria-pressed')).toBe('true');
    expect(rows()[1].getAttribute('aria-pressed')).toBe('false');
    fireEvent.click(rows()[0]);
    expect(rows()[0].getAttribute('aria-pressed')).toBe('false');
    fireEvent.click(rows()[1]);
    fireEvent.keyDown(rows()[0], { key: 'Enter' });
    expect(rows()[0].getAttribute('aria-pressed')).toBe('true');
    expect(rows()[1].getAttribute('aria-pressed')).toBe('false');
    fireEvent.click(screen.getByRole('button', { name: 'Clear log' }));
    await tick();
    expect(document.querySelector('[data-pinned]')).toBeNull();
  });

  it('stderr lines carry the stderr tag', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'out'), logEvent(C_A, 2, 1, 'err', 'api', 'stderr')] });
    mount();
    await tick();
    expect(within(rows()[0]).queryByText('stderr')).toBeNull();
    expect(within(rows()[1]).getByText('stderr')).toBeTruthy();
  });

  it('service colours follow the sorted index', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'x', 'worker')] });
    mount({ kind: 'stack', name: 'web' }, ['worker', 'api']);
    await tick();
    const chips = within(screen.getByLabelText('Service filter'));
    expect(chips.getByRole('button', { name: 'api' }).querySelector('i')!.getAttribute('style')).toContain('--series-1');
    expect(chips.getByRole('button', { name: 'worker' }).querySelector('i')!.getAttribute('style')).toContain('--series-2');
    expect(within(rows()[0]).getByText('worker').getAttribute('style')).toContain('--series-2');
  });

  it('service colours use the metrics series slots, so the fifth service is series 5', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'x', 'e')] });
    mount({ kind: 'stack', name: 'web' }, ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i']);
    await tick();
    const chips = within(screen.getByLabelText('Service filter'));
    expect(chips.getByRole('button', { name: 'e' }).querySelector('i')!.getAttribute('style')).toContain('--series-5');
    expect(chips.getByRole('button', { name: 'h' }).querySelector('i')!.getAttribute('style')).toContain('--series-8');
    expect(chips.getByRole('button', { name: 'i' }).querySelector('i')!.getAttribute('style')).toContain('--series-1');
    expect(within(rows()[0]).getByText('e').getAttribute('style')).toContain('--series-5');
  });

  it('the service chip row and the level control name their All buttons apart', async () => {
    fake = installFakeStream({ [PATH]: [] });
    mount();
    await tick();
    const all = within(screen.getByLabelText('Service filter')).getByRole('button', { name: 'All services' });
    expect(all.textContent).toBe('all');
    const level = within(screen.getByRole('group', { name: 'Level' })).getByRole('button', { name: 'All levels' });
    expect(level.textContent).toBe('All');
  });

  it('auto-scroll sticks to the bottom only when near it', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'one')] });
    mount();
    await tick();
    const el = scroller();
    Object.defineProperty(el, 'scrollHeight', { configurable: true, value: 1000 });
    Object.defineProperty(el, 'clientHeight', { configurable: true, value: 200 });
    el.scrollTop = 100;
    fireEvent.scroll(el);
    await act(async () => {
      fake.push(PATH, logEvent(C_A, 2, 1, 'two'));
      await vi.advanceTimersByTimeAsync(200);
    });
    expect(el.scrollTop).toBe(100);
    el.scrollTop = 790;
    fireEvent.scroll(el);
    await act(async () => {
      fake.push(PATH, logEvent(C_A, 3, 1, 'three'));
      await vi.advanceTimersByTimeAsync(200);
    });
    expect(el.scrollTop).toBe(1000);
  });

  it('resuming after scrolling up jumps to the bottom', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'one')] });
    mount();
    await tick();
    const el = scroller();
    Object.defineProperty(el, 'scrollHeight', { configurable: true, value: 1000 });
    Object.defineProperty(el, 'clientHeight', { configurable: true, value: 200 });
    el.scrollTop = 100;
    fireEvent.scroll(el);
    fireEvent.click(screen.getByRole('button', { name: 'Following' }));
    await act(async () => {
      fake.push(PATH, logEvent(C_A, 2, 1, 'two'));
      await vi.advanceTimersByTimeAsync(200);
    });
    fireEvent.click(screen.getByRole('button', { name: 'Paused · 1 new' }));
    expect(el.scrollTop).toBe(1000);
  });

  it('debounces the search by 150 ms and clears it immediately', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'boom'), logEvent(C_A, 2, 1, 'quiet')] });
    const { container } = mount();
    await tick();
    const input = screen.getByLabelText('Search logs') as HTMLInputElement;
    fireEvent.change(input, { target: { value: 'boom' } });
    expect(input.value).toBe('boom');
    expect(rows()).toHaveLength(2);
    expect(container.querySelector('mark')).toBeNull();
    await tick(149);
    expect(rows()).toHaveLength(2);
    await tick(1);
    expect(rows()).toHaveLength(1);
    fireEvent.change(input, { target: { value: '' } });
    expect(rows()).toHaveLength(2);
  });

  it('has a single tab stop among rows and moves it with the arrow keys', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'one'), logEvent(C_A, 2, 1, 'two'), logEvent(C_A, 3, 1, 'three')] });
    mount();
    await tick();
    const tabbable = () => rows().filter((r) => r.getAttribute('tabindex') === '0');
    expect(tabbable()).toEqual([rows()[2]]);
    rows()[2].focus();
    fireEvent.keyDown(rows()[2], { key: 'ArrowUp' });
    expect(document.activeElement).toBe(rows()[1]);
    expect(tabbable()).toEqual([rows()[1]]);
    fireEvent.keyDown(rows()[1], { key: 'Home' });
    expect(document.activeElement).toBe(rows()[0]);
    fireEvent.keyDown(rows()[0], { key: 'End' });
    expect(document.activeElement).toBe(rows()[2]);
    fireEvent.keyDown(rows()[2], { key: 'ArrowDown' });
    expect(document.activeElement).toBe(rows()[2]);
    fireEvent.keyDown(rows()[2], { key: ' ' });
    expect(rows()[2].getAttribute('aria-pressed')).toBe('true');
  });

  it('the pinned row is the tab stop', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'one'), logEvent(C_A, 2, 1, 'two')] });
    mount();
    await tick();
    fireEvent.click(rows()[0]);
    document.body.focus();
    expect(rows().filter((r) => r.getAttribute('tabindex') === '0')).toEqual([rows()[0]]);
  });

  it('flashes only genuinely new lines, not rows revealed by a filter', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'old')] });
    mount();
    await tick();
    expect(rows()[0].hasAttribute('data-new')).toBe(true);
    await tick(2000);
    fireEvent.click(screen.getByRole('button', { name: 'Errors' }));
    fireEvent.click(within(screen.getByRole('group', { name: 'Level' })).getByRole('button', { name: 'All levels' }));
    expect(rows()[0].hasAttribute('data-new')).toBe(false);
    await act(async () => {
      fake.push(PATH, logEvent(C_A, 2, 1, 'fresh'));
      await vi.advanceTimersByTimeAsync(200);
    });
    expect(rows()[0].hasAttribute('data-new')).toBe(false);
    expect(rows()[1].hasAttribute('data-new')).toBe(true);
  });

  it('End focuses the last row even when an end-of-log marker follows the rows', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'one'), logEvent(C_A, 2, 1, 'two'), 'event: end\ndata: {}\n\n'] });
    mount();
    await tick();
    expect(screen.getByText(/end of log/i)).toBeTruthy();
    rows()[0].focus();
    fireEvent.keyDown(rows()[0], { key: 'End' });
    expect(document.activeElement).toBe(rows()[1]);
  });
});
