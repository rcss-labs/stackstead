import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import type { StackDetail } from '$lib/api/types';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';

// Keep the fallback diff: the CodeMirror chunk is exercised in the DiffView tests.
vi.mock('$lib/stacks/cm', () => ({
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));
vi.mock('$lib/stores/stacks', async (importActual) => ({
  ...(await importActual<object>()),
  refreshStacks: vi.fn(async () => {}),
}));

import { refreshStacks } from '$lib/stores/stacks';
import { AddServicePanel } from './AddServicePanel';

const stack = { name: 'web', hash: 'h1', services: [] } as unknown as StackDetail;
let fake: FakeFetch;
let routes: Parameters<typeof installFakeFetch>[0];

beforeEach(() => {
  vi.mocked(refreshStacks).mockClear();
  toasts.reset();
  routes = {
    'GET /api/networks': { body: [] },
    'GET /api/stacks/web/compose': { body: { content: 'old', hash: 'h1' } },
    'POST /api/stacks/web/services/preview': { body: { content: 'new', hash: 'h1' } },
    'POST /api/stacks/web/services': { body: { ...stack, hash: 'h2', services: [{ name: 'cache' }] }, headers: { 'X-Stackstead-Checks': 'partial' } },
  };
  fake = installFakeFetch(routes);
});
afterEach(async () => {
  cleanup();
  fake.restore();
  await new Promise((r) => setTimeout(r, 5)); // useStore releases its subscriptions on the next task
});

const noop = () => {};
async function fill() {
  const u = userEvent.setup();
  await u.type(screen.getByLabelText('Service name'), 'cache');
  await u.type(screen.getByLabelText('Image'), 'redis:7');
  await u.click(screen.getByRole('button', { name: 'Preview changes' }));
  await screen.findByRole('heading', { name: 'Review the service to add' });
  return u;
}

describe('AddServicePanel', () => {
  it('renders name/form, shows review and Back, and passes successful detail/partial checks', async () => {
    const onAdded = vi.fn();
    render(<AddServicePanel stack={stack} onAdded={onAdded} onCancel={noop} onReload={async () => stack} />);
    const u = await fill();
    expect(screen.getByTestId('diff-view')).toBeTruthy();
    await u.click(screen.getByRole('button', { name: 'Back to editing' }));
    expect(screen.getByLabelText('Service name')).toHaveProperty('value', 'cache');
    await u.click(screen.getByRole('button', { name: 'Preview changes' }));
    await u.click(await screen.findByRole('button', { name: 'Save' }));
    await waitFor(() => expect(onAdded).toHaveBeenCalledWith(expect.objectContaining({ hash: 'h2' }), true));
  });

  it('Cancel invokes parent and conflict offers Reload / Discard', async () => {
    routes['POST /api/stacks/web/services'] = { status: 409, body: { error: 'changed on disk' } };
    const onCancel = vi.fn();
    render(<AddServicePanel stack={stack} onAdded={noop} onCancel={onCancel} onReload={async () => stack} />);
    const u = await fill();
    await u.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Save refused')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Reload' })).toBeTruthy();
    await u.click(screen.getByRole('button', { name: 'Discard my changes' }));
    await waitFor(() => expect(screen.getByLabelText('Service name')).toHaveProperty('value', ''));
    await u.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it('refreshes authoritative list but suppresses callback after unmount during save', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    routes['POST /api/stacks/web/services'] = async () => {
      await gate;
      return { body: stack };
    };
    const onAdded = vi.fn();
    const view = render(<AddServicePanel stack={stack} onAdded={onAdded} onCancel={noop} onReload={async () => stack} />);
    const u = await fill();
    await u.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(fake.calls.some((c) => c.method === 'POST' && c.path.endsWith('/services'))).toBe(true));
    view.unmount();
    release();
    await waitFor(() => expect(refreshStacks).toHaveBeenCalledOnce());
    expect(onAdded).not.toHaveBeenCalled();
  });

  it('uses the reviewed name for next-Up guidance even if returned detail has no new service', async () => {
    routes['POST /api/stacks/web/services'] = { body: stack };
    render(<AddServicePanel stack={stack} onAdded={noop} onCancel={noop} onReload={async () => stack} />);
    const u = await fill();
    await u.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(get(toasts).some((t) => t.message === 'Added cache to compose.yaml. Run Up to start it.')).toBe(true));
  });

  it('reports dirty to the parent as the name is typed', async () => {
    const onDirtyChange = vi.fn();
    render(<AddServicePanel stack={stack} onAdded={noop} onCancel={noop} onReload={async () => stack} onDirtyChange={onDirtyChange} />);
    await userEvent.setup().type(screen.getByLabelText('Service name'), 'c');
    await waitFor(() => expect(onDirtyChange).toHaveBeenLastCalledWith(true));
  });
});
