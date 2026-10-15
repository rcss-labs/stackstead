import { render, screen, fireEvent, within } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { installFakeStream, type FakeStream } from '../../vitest-setup';
import LogsViewer from './LogsViewer.svelte';

const PATH = '/api/stacks/web/logs';
const C_A = 'aaaaaaaaaaaa';
let fake: FakeStream;
const tick = (ms = 50) => vi.advanceTimersByTimeAsync(ms);

function logEvent(container: string, ns: number, ord: number, text: string, service = 'api', stream = 'stdout') {
  const data = JSON.stringify({ service, container, stream, time: '2026-10-14T10:00:00Z', text });
  return `id: ${container}:${ns}:${ord}\nevent: log\ndata: ${data}\n\n`;
}
const rows = (c: HTMLElement) => c.querySelectorAll('[data-testid="log-row"]');

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  fake?.restore();
  vi.useRealTimers();
});

describe('LogsViewer', () => {
  it('TestFloodRendersBoundedRows', async () => {
    const flood = Array.from({ length: 20000 }, (_, i) => logEvent(C_A, i + 1, 1, `line ${i}`)).join('');
    fake = installFakeStream({ [PATH]: [flood] });
    const { container } = render(LogsViewer, { stackName: 'web', services: ['api'] });
    await tick(200);
    expect(rows(container).length).toBeGreaterThan(0);
    expect(rows(container).length).toBeLessThanOrEqual(5000);
    expect(screen.getByText(/15[, ]?000 older lines dropped/)).toBeTruthy();
  });

  it('renders text as text and filters by search', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, '<script>alert(1)</script> boom'), logEvent(C_A, 2, 1, 'quiet')] });
    const { container } = render(LogsViewer, { stackName: 'web', services: ['api'] });
    await tick();
    expect(container.querySelector('span')!.textContent).toBe('<script>alert(1)</script> boom');
    expect(container.querySelector('script')).toBeNull();
    await fireEvent.input(screen.getByLabelText('Search logs'), { target: { value: 'BOOM' } });
    await tick(200);
    expect(rows(container)).toHaveLength(1);
    expect(container.querySelector('mark')!.textContent).toBe('boom');
  });

  it('filters by stream', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'out'), logEvent(C_A, 2, 1, 'err', 'api', 'stderr')] });
    const { container } = render(LogsViewer, { stackName: 'web', services: ['api'] });
    await tick();
    await fireEvent.change(screen.getByLabelText('Stream'), { target: { value: 'stderr' } });
    expect(rows(container)).toHaveLength(1);
  });

  it('service change restarts the stream with the new query', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'x')] });
    const { container } = render(LogsViewer, { stackName: 'web', services: ['api', 'worker'] });
    await tick();
    await fireEvent.change(screen.getByLabelText('Service'), { target: { value: 'worker' } });
    await tick();
    expect(fake.requests.at(-1)!.path).toContain('service=worker');
    expect(fake.requests.at(-1)!.headers.get('Last-Event-ID')).toBeNull();
    expect(rows(container)).toHaveLength(1); // only the new stream's line
  });

  it('shows per-container errors, dismissible', async () => {
    const err = JSON.stringify({ service: 'api', container: C_A, error: 'gone <b>x</b>' });
    fake = installFakeStream({ [PATH]: [`event: error\ndata: ${err}\n\n`] });
    render(LogsViewer, { stackName: 'web', services: ['api'] });
    await tick();
    const alert = screen.getByRole('alert');
    expect(alert.textContent).toContain('gone <b>x</b>');
    await fireEvent.click(within(alert).getByRole('button', { name: /dismiss/i }));
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('shows end of log when the stream ends', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'x'), 'event: end\ndata: {}\n\n'] });
    render(LogsViewer, { stackName: 'web', services: ['api'] });
    await tick();
    expect(screen.getByText(/end of log/i)).toBeTruthy();
  });

  it('pause freezes the view and counts new lines', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'first')] });
    const { container } = render(LogsViewer, { stackName: 'web', services: ['api'] });
    await tick();
    await fireEvent.click(screen.getByRole('button', { name: 'Pause' }));
    fake.push(PATH, logEvent(C_A, 2, 1, 'second') + logEvent(C_A, 3, 1, 'third'));
    await tick();
    expect(rows(container)).toHaveLength(1);
    expect(screen.getByText(/2 new lines/)).toBeTruthy();
    await fireEvent.click(screen.getByRole('button', { name: 'Resume' }));
    expect(rows(container)).toHaveLength(3);
  });

  it('warns when tail is all', async () => {
    fake = installFakeStream({ [PATH]: [] });
    render(LogsViewer, { stackName: 'web', services: ['api'] });
    await tick();
    await fireEvent.change(screen.getByLabelText('Tail'), { target: { value: 'all' } });
    expect(screen.getByText(/entire log history/i)).toBeTruthy();
  });

  it('labels dismiss buttons and keeps the log region quiet for screen readers', async () => {
    const err = JSON.stringify({ service: 'api', container: 'web-api-1', error: 'oops' });
    fake = installFakeStream({ [PATH]: [`event: error\ndata: ${err}\n\n`] });
    const { container } = render(LogsViewer, { stackName: 'web', services: ['api'] });
    await tick();
    expect(screen.getByRole('button', { name: 'Dismiss error for web-api-1' })).toBeTruthy();
    expect(container.querySelector('[role="log"]')!.getAttribute('aria-live')).toBe('off');
  });

  it('offers Reconnect after the stream ends and Clear resets the new-lines count', async () => {
    fake = installFakeStream({ [PATH]: { responses: [{ chunks: [logEvent(C_A, 1, 1, 'a'), 'event: end\ndata: {}\n\n'] }, { chunks: [] }] } });
    render(LogsViewer, { stackName: 'web', services: ['api'] });
    await tick();
    await fireEvent.click(screen.getByRole('button', { name: 'Reconnect' }));
    await tick();
    expect(fake.requests).toHaveLength(2);
    expect(screen.queryByRole('button', { name: 'Reconnect' })).toBeNull();
  });

  it('restart while paused unpauses', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'a')] });
    render(LogsViewer, { stackName: 'web', services: ['api', 'worker'] });
    await tick();
    await fireEvent.click(screen.getByRole('button', { name: 'Pause' }));
    await fireEvent.change(screen.getByLabelText('Service'), { target: { value: 'worker' } });
    expect(screen.getByRole('button', { name: 'Pause' })).toBeTruthy();
  });

  it('service prop preselects the stream filter', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'x')] });
    render(LogsViewer, { stackName: 'web', services: ['api', 'worker'], service: 'worker' });
    await tick();
    expect(fake.requests[0]!.path).toContain('service=worker');
    expect((screen.getByLabelText('Service') as HTMLSelectElement).value).toBe('worker');
  });

  it('unknown service prop falls back to all services', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'x')] });
    render(LogsViewer, { stackName: 'web', services: ['api', 'worker'], service: 'nope' });
    await tick();
    expect(fake.requests[0]!.path).not.toContain('service=');
    expect((screen.getByLabelText('Service') as HTMLSelectElement).value).toBe('');
  });

  it('shows the live status text', async () => {
    fake = installFakeStream({ [PATH]: [logEvent(C_A, 1, 1, 'x')] });
    render(LogsViewer, { stackName: 'web', services: ['api'] });
    await tick();
    expect(screen.getByRole('status').textContent).toContain('live · following');
    await fireEvent.click(screen.getByRole('button', { name: 'Pause' }));
    expect(screen.getByRole('status').textContent).toContain('paused');
  });
});
