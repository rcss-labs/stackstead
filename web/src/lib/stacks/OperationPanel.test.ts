import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream } from '../../vitest-setup';
import OperationPanel from './OperationPanel.svelte';

const PATH = '/api/operations/op1/stream';
const line = (id: string, text: string) => `id: ${id}\nevent: line\ndata: ${JSON.stringify(text)}\n\n`;
const op = (state: string) => ({ id: 'op1', stack: 'web', action: 'up', state, startedAt: '2026-01-01T00:00:00Z', exitCode: state === 'succeeded' ? 0 : undefined });
const doneEv = (state = 'succeeded') => `event: done\ndata: ${JSON.stringify(op(state))}\n\n`;

let stream: FakeStream;
let fetcher: FakeFetch;
afterEach(() => {
  stream?.restore();
  fetcher?.restore();
});

function setup(chunks: string[], props: Record<string, unknown> = {}) {
  fetcher = installFakeFetch({ 'GET /api/operations/op1': { body: { ...op('running'), lines: [] } } });
  stream = installFakeStream({ [PATH]: chunks });
  return render(OperationPanel, { operationId: 'op1', ...props });
}

describe('OperationPanel', () => {
  it('renders lines as text in a log region with header details', async () => {
    setup([line('0', 'pulling <b>x</b>'), line('1', 'created')]);
    const log = await screen.findByRole('log');
    await waitFor(() => expect(log.textContent).toContain('created'));
    expect(log.textContent).toContain('pulling <b>x</b>');
    expect(log.querySelector('b')).toBeNull();
    expect(log.getAttribute('aria-live')).toBe('off');
    await waitFor(() => expect(screen.getByText('web')).toBeTruthy());
  });

  it('shows Cancel while running and calls oncancel with the id', async () => {
    const oncancel = vi.fn();
    setup([line('0', 'a')], { oncancel });
    const btn = await screen.findByRole('button', { name: 'Cancel' });
    await userEvent.setup().click(btn);
    expect(oncancel).toHaveBeenCalledWith('op1');
  });

  it('on done: calls onfinished, shows state and exit code, hides Cancel', async () => {
    const onfinished = vi.fn();
    setup([line('0', 'a'), doneEv()], { onfinished });
    await waitFor(() => expect(onfinished).toHaveBeenCalledTimes(1));
    expect(onfinished.mock.calls[0][0]).toMatchObject({ id: 'op1', state: 'succeeded' });
    expect(screen.getByText('succeeded')).toBeTruthy();
    expect(screen.getByText(/exit code 0/i)).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull();
  });

  it('shows the dropped note dimmed as the first line', async () => {
    setup([`event: line\ndata: ${JSON.stringify('... 3 earlier lines dropped')}\n\n`, line('3', 'a'), doneEv()]);
    const note = await screen.findByText('... 3 earlier lines dropped');
    expect(note.classList.contains('note')).toBe(true);
  });
});
