import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { MemoryRouter, useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { api } from '$lib/api/client';
import type { Operation, StackSummary } from '$lib/api/types';
import { effectiveTheme, setTheme, theme } from '$lib/stores/appearance';
import { changes, live, startLive, stopLive } from '$lib/stores/live';
import { startOperationTracker } from '$lib/stores/operationTracker';
import { runningOps, startRunningOps } from '$lib/stores/runningOps';
import { operationsRefresh, refreshRunningOps, startStacks, stacks } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream } from '../../vitest-setup';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { useGlobalShortcut } from '../hooks/useGlobalShortcut';
import { startPeers } from '$lib/servers/peers';
import { PaletteHost } from './PaletteHost';

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function stack(name: string, status: StackSummary['status'], running: number, serviceCount: number): StackSummary {
  return {
    name,
    path: `/srv/${name}/compose.yaml`,
    project: name,
    origin: 'managed',
    status,
    serviceCount,
    running,
    health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
    ports: [],
    libraryUpdates: 0,
  };
}

const operation: Operation = { id: 'op1', stack: 'web', action: 'restart', state: 'running', startedAt: '2026-10-19T10:00:00Z' };

let fake: FakeFetch;
let stream: FakeStream;
let composeStatus: string;
let runningNow: Operation[];
let operationState: Operation['state'];

beforeEach(() => {
  toasts.reset();
  composeStatus = '2.29.0';
  runningNow = [];
  operationState = 'running';
  fake = installFakeFetch({
    'GET /api/stacks': { body: [stack('web', 'running', 2, 2), stack('db', 'stopped', 0, 1)] },
    'GET /api/fleet/stacks': {
      body: {
        servers: [
          { id: 'self', name: 'this server', slug: '', local: true, status: 'reachable', error: '', lastSeenAt: null },
          { id: 'b1', name: 'mini-02', slug: 'mini-02', local: false, status: 'reachable', error: '', lastSeenAt: null },
        ],
        stacks: [{ ...stack('pihole', 'running', 1, 1), server: 'b1' }],
        events: [],
      },
    },
    'GET /api/status': () => ({ body: { docker: 'ok', compose: composeStatus, events: 'connected', metricsDegraded: false } }),
    'GET /api/operations?state=running': () => ({ body: runningNow }),
    'GET /api/operations/op1': () => ({ body: { ...operation, state: operationState, lines: [] } }),
    'POST /api/stacks/web/actions/restart': { body: operation },
  });
  stream = installFakeStream({ '/api/events': [] });
});

afterEach(async () => {
  cleanup();
  stopLive();
  stream.restore();
  fake.restore();
  setTheme('system');
  // useStore releases its store subscriptions on the next task after unmount.
  await sleep(5);
});

function Where() {
  return <span data-testid="where">{useLocation().pathname}</span>;
}

/** The app's wiring in miniature: the shortcut hook opens the lazily loaded palette. */
function Harness({
  underlying = false,
  onUnderlyingCancel = () => {},
  runningOps = [],
}: {
  underlying?: boolean;
  onUnderlyingCancel?: () => void;
  runningOps?: Operation[];
}) {
  const [open, setOpen] = useState(false);
  useGlobalShortcut(() => setOpen(true));
  return (
    <>
      <input aria-label="Elsewhere" />
      <button onClick={() => setOpen(true)}>Open palette</button>
      <ConfirmDialog open={underlying} title="Underneath" message="m" confirmLabel="Ok" onConfirm={() => {}} onCancel={onUnderlyingCancel} />
      <PaletteHost open={open} runningOps={runningOps} onClose={() => setOpen(false)} />
    </>
  );
}

async function renderPalette(props: Parameters<typeof Harness>[0] = {}) {
  const stop = startStacks({ list: api.stacks, changes });
  await waitFor(() => expect(get(stacks).items).toHaveLength(2));
  const user = userEvent.setup();
  render(
    <MemoryRouter initialEntries={['/settings']}>
      <Harness {...props} />
      <Where />
    </MemoryRouter>,
  );
  afterEachStops.push(stop);
  return user;
}
const afterEachStops: Array<() => void> = [];
afterEach(() => {
  afterEachStops.splice(0).forEach((stop) => stop());
});

// Resolves once the real palette (not the loading stand-in) is showing.
const palette = async () => {
  await screen.findByRole('combobox');
  return screen.findByRole('dialog', { name: 'Command palette' });
};
const search = () => screen.getByRole('combobox');
const where = () => screen.getByTestId('where').textContent;

describe('CommandPalette', () => {
  it('opens on Cmd+K and Ctrl+K even while typing in an input, and the search field is focused', async () => {
    const user = await renderPalette();
    await user.click(screen.getByLabelText('Elsewhere'));
    await user.keyboard('{Meta>}k{/Meta}');
    await palette();
    expect(document.activeElement).toBe(search());
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await user.click(screen.getByLabelText('Elsewhere'));
    await user.keyboard('{Control>}k{/Control}');
    await palette();
    expect(document.activeElement).toBe(search());
  });

  it('Escape closes the palette and nothing else', async () => {
    const onUnderlyingCancel = vi.fn();
    const user = await renderPalette({ underlying: true, onUnderlyingCancel });
    await user.keyboard('{Meta>}k{/Meta}');
    await palette();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Command palette' })).toBeNull());
    expect(onUnderlyingCancel).not.toHaveBeenCalled();
    expect(screen.getByRole('alertdialog', { name: 'Underneath' })).toBeTruthy();
  });

  it('arrow keys move aria-activedescendant and Enter navigates', async () => {
    const user = await renderPalette();
    await user.keyboard('{Meta>}k{/Meta}');
    await palette();
    const options = screen.getAllByRole('option');
    expect(options[0].getAttribute('aria-selected')).toBe('true');
    expect(search().getAttribute('aria-activedescendant')).toBe(options[0].id);
    await user.keyboard('{ArrowDown}');
    expect(search().getAttribute('aria-activedescendant')).toBe(options[1].id);
    expect(options[1].getAttribute('aria-selected')).toBe('true');
    await user.keyboard('{Enter}');
    expect(where()).toBe('/containers');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('hover selects an entry', async () => {
    const user = await renderPalette();
    await user.keyboard('{Meta>}k{/Meta}');
    await palette();
    const options = screen.getAllByRole('option');
    await user.hover(options[3]);
    expect(search().getAttribute('aria-activedescendant')).toBe(options[3].id);
    expect(options[3].getAttribute('aria-selected')).toBe('true');
  });

  it('initialQuery seeds the input and filters the results', async () => {
    await renderPalette();
    const { default: Palette } = await import('./CommandPalette');
    cleanup();
    render(
      <MemoryRouter>
        <Palette open runningOps={[]} onClose={() => {}} initialQuery="we" />
      </MemoryRouter>,
    );
    const input = (await screen.findByRole('combobox', { name: 'Search or run a command' })) as HTMLInputElement;
    expect(input.value).toBe('we');
    const list = screen.getByRole('listbox', { name: 'Results' });
    expect(within(list).getByText('web')).toBeTruthy();
    expect(within(list).queryByText('db')).toBeNull();
  });

  it('typing filters results and shows No matches', async () => {
    const user = await renderPalette();
    await user.keyboard('{Meta>}k{/Meta}');
    await palette();
    await user.type(search(), 'netw');
    expect(screen.getAllByRole('option').map((o) => within(o).getByText(/Networks/).textContent)).toEqual(['Networks']);
    await user.clear(search());
    await user.type(search(), 'zzzz');
    expect(screen.queryAllByRole('option')).toHaveLength(0);
    expect(screen.getByText('No matches')).toBeTruthy();
  });

  it('Enter on a stack entry navigates to its detail', async () => {
    const user = await renderPalette();
    await user.keyboard('{Meta>}k{/Meta}');
    await palette();
    await user.type(search(), 'db');
    await user.keyboard('{Enter}');
    expect(where()).toBe('/stacks/db');
  });

  it('running an action posts the lifecycle request, closes and pushes a pending toast with a View link that resolves when the operation ends', async () => {
    const stopOps = startRunningOps({ list: api.runningOperations, changes, refresh: operationsRefresh });
    const stopTracker = startOperationTracker({ runningOps, get: api.operation });
    afterEachStops.push(stopTracker, stopOps);
    const user = await renderPalette();
    await user.keyboard('{Meta>}k{/Meta}');
    await palette();
    await user.type(search(), 'restart web');
    await user.keyboard('{Enter}');
    await waitFor(() => expect(fake.calls.some((c) => c.method === 'POST' && c.path === '/api/stacks/web/actions/restart')).toBe(true));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    await waitFor(() => expect(get(toasts)).toHaveLength(1));
    expect(get(toasts)[0]).toMatchObject({
      kind: 'info',
      message: 'restart · web',
      pending: true,
      link: { label: 'View', href: '/stacks/web?tab=operations&op=op1' },
    });
    runningNow = [operation];
    refreshRunningOps();
    await waitFor(() => expect(get(runningOps)).toHaveLength(1));
    runningNow = [];
    operationState = 'succeeded';
    refreshRunningOps();
    await waitFor(() => expect(get(toasts)[0]).toMatchObject({ kind: 'success', message: 'restart · web succeeded' }));
    expect(get(toasts)).toHaveLength(1);
  });

  it('a failed action pushes a danger toast with the error message', async () => {
    fake.restore();
    fake = installFakeFetch({
      'GET /api/stacks': { body: [stack('web', 'running', 2, 2), stack('db', 'stopped', 0, 1)] },
      'GET /api/operations?state=running': { body: [] },
      'POST /api/stacks/web/actions/restart': { status: 409, body: { error: 'stack is busy' } },
    });
    const user = await renderPalette();
    await user.keyboard('{Meta>}k{/Meta}');
    await palette();
    await user.type(search(), 'restart web');
    await user.keyboard('{Enter}');
    await waitFor(() => expect(get(toasts)).toHaveLength(1));
    expect(get(toasts)[0]).toMatchObject({ kind: 'danger', message: 'stack is busy' });
  });

  it('a disabled action shows its reason and does not post', async () => {
    composeStatus = 'unavailable';
    startLive({ fetchStatus: api.status });
    await waitFor(() => expect(get(live).compose).toBe('unavailable'));
    const user = await renderPalette();
    await user.keyboard('{Meta>}k{/Meta}');
    await palette();
    await user.type(search(), 'restart web');
    const option = screen.getByRole('option', { name: /Restart web/ });
    expect(option.getAttribute('aria-disabled')).toBe('true');
    expect(within(option).getByText('docker compose plugin not found')).toBeTruthy();
    await user.keyboard('{Enter}');
    await user.click(option);
    expect(fake.calls.some((c) => c.method === 'POST')).toBe(false);
    expect(screen.getByRole('dialog', { name: 'Command palette' })).toBeTruthy();
    expect(document.activeElement).toBe(search());
  });

  it('a stack busy before the palette opens has its actions disabled on the first render and never posts', async () => {
    const user = await renderPalette({ runningOps: [operation] });
    await user.keyboard('{Meta>}k{/Meta}');
    await palette();
    fireEvent.change(search(), { target: { value: 'restart web' } });
    const option = screen.getByRole('option', { name: /Restart web/ });
    expect(option.getAttribute('aria-disabled')).toBe('true');
    expect(within(option).getByText('an operation is already running')).toBeTruthy();
    await user.keyboard('{Enter}');
    await user.click(option);
    expect(fake.calls.some((c) => c.method === 'POST')).toBe(false);
  });

  it('clicking the backdrop closes the palette; clicking inside the panel does not', async () => {
    const user = await renderPalette();
    await user.keyboard('{Meta>}k{/Meta}');
    const panel = await palette();
    await user.click(search());
    await user.click(panel);
    expect(screen.getByRole('dialog', { name: 'Command palette' })).toBeTruthy();
    await user.click(panel.parentElement!);
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('Toggle theme flips the theme and closes', async () => {
    setTheme('dark');
    const user = await renderPalette();
    await user.keyboard('{Meta>}k{/Meta}');
    await palette();
    await user.type(search(), 'toggle theme');
    await user.keyboard('{Enter}');
    expect(get(theme)).toBe('light');
    expect(get(effectiveTheme)).toBe('light');
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  });

  it('lists connected servers\' stacks with the server name and opens one', async () => {
    const stopPeers = startPeers({ list: async () => [{ id: 'b1', name: 'mini-02', slug: 'mini-02', url: '', version: '1.5', status: 'reachable', stackCount: 1, lastSeenAt: null, lastError: '', compatible: true, plaintext: false }] });
    afterEachStops.push(stopPeers);
    const user = await renderPalette();
    await user.keyboard('{Control>}k{/Control}');
    await palette();
    await user.type(search(), 'pihole');
    const option = await screen.findByRole('option', { name: /pihole.*mini-02/ });
    expect(fake.calls.filter((c) => c.path === '/api/fleet/stacks')).toHaveLength(1);
    await user.click(option);
    expect(where()).toBe('/s/mini-02/stacks/pihole');
  });
});

