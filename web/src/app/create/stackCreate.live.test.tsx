import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { unsavedChanges } from '$lib/stores/guard';
import * as liveModule from '$lib/stores/live';
import { toasts } from '$lib/stores/toasts';
import { installFakeStream } from '../../vitest-setup';
import { row } from '../containers/containersTestKit';
import { fake, fillImage, location, mountAt, post, routes, submit } from './createTestKit';

vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const store = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  const changes = writable<{ project: string; seq: number; resync?: true } | null>(null);
  return {
    ...(await orig<typeof import('$lib/stores/live')>()),
    live: { subscribe: store.subscribe },
    changes: { subscribe: changes.subscribe },
    refreshStatus: vi.fn(),
    __changes: changes,
  };
});
const emitChange = (c: { project: string; seq: number; resync?: true }) =>
  act(() => (liveModule as unknown as { __changes: { set: (v: unknown) => void } }).__changes.set(c));

import { App } from '../App';

const HOME_ASSISTANT = row('ha', { stack: 'home-assistant', ports: ['0.0.0.0:8123->8123/tcp'] });

let mounted: ReturnType<typeof mountAt> | undefined;
async function mount(extra: Record<string, unknown> = {}) {
  routes(extra);
  mounted = mountAt('/stacks/new');
  await screen.findByLabelText('Stack name'); // the screen is a lazy chunk
  return userEvent.setup();
}

afterEach(async () => {
  cleanup();
  await new Promise((r) => setTimeout(r, 5)); // useStore releases its subscriptions on the next task
  mounted = undefined;
  fake?.restore();
  toasts.reset();
  vi.unstubAllGlobals();
});

const addPort = async (user: ReturnType<typeof userEvent.setup>) => user.click(screen.getByRole('button', { name: 'Add port' }));
const preview = () => within(screen.getByRole('region', { name: 'compose.yaml' })).getByText(/^services:/, { selector: 'pre' });

describe('New stack port check', () => {
  it('shows a conflict from a published container port and a free note otherwise', async () => {
    const user = await mount({ 'GET /api/containers': { body: [HOME_ASSISTANT] } });
    await addPort(user);
    await user.type(screen.getByLabelText('Port 1'), '8123:8123');
    expect(await screen.findByText('Port 8123 is used by home-assistant')).toBeTruthy();
    expect(screen.getByLabelText('Port 1').getAttribute('data-check')).toBe('danger');
    await user.clear(screen.getByLabelText('Port 1'));
    await user.type(screen.getByLabelText('Port 1'), '3001:3001');
    expect(await screen.findByText('Port 3001 is free')).toBeTruthy();
    expect(screen.getByLabelText('Port 1').getAttribute('data-check')).toBe('success');
  });

  it('a port conflict is advisory: the request is still sent and the input is not invalid', async () => {
    const user = await mount({ 'GET /api/containers': { body: [HOME_ASSISTANT] } });
    await fillImage(user);
    await addPort(user);
    await user.type(screen.getByLabelText('Port 1'), '8123:8123');
    await screen.findByText('Port 8123 is used by home-assistant');
    expect(screen.getByLabelText('Port 1').getAttribute('aria-invalid')).toBeNull();
    await submit(user);
    await waitFor(() => expect(post()).toBeDefined());
  });

  it('shows no port note until the containers list has loaded', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    const user = await mount({
      'GET /api/containers': async () => {
        await gate;
        return { body: [] };
      },
    });
    await addPort(user);
    await user.type(screen.getByLabelText('Port 1'), '8080:80');
    expect(screen.queryByText(/is free|is used/)).toBeNull();
    release();
    expect(await screen.findByText('Port 8080 is free')).toBeTruthy();
  });

  it('the check stays quiet when the containers list fails', async () => {
    const user = await mount({ 'GET /api/containers': { status: 500, body: { error: 'boom' } } });
    await addPort(user);
    await user.type(screen.getByLabelText('Port 1'), '8080:80');
    await waitFor(() => expect(fake.calls.some((c) => c.path === '/api/containers')).toBe(true));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText(/is free|is used/)).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('flags a port two cards publish and names the earlier card', async () => {
    const user = await mount();
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await addPort(user);
    await user.type(screen.getByLabelText('Port 1'), '8080:80');
    await user.click(screen.getByRole('button', { name: '+ Add another service' }));
    await user.type(screen.getByLabelText('Service name'), 'db');
    await addPort(user);
    await user.type(screen.getByLabelText('Port 1'), '8080:5432');
    expect(await screen.findByText('Port 8080 is also published by web in this stack')).toBeTruthy();
    expect(screen.getByLabelText('Port 1').getAttribute('data-check')).toBe('warning');
  });

  it('flags a duplicate port between two unnamed cards', async () => {
    const user = await mount();
    await addPort(user);
    await user.type(screen.getByLabelText('Port 1'), '8080:80');
    await user.click(screen.getByRole('button', { name: '+ Add another service' }));
    await addPort(user);
    await user.type(screen.getByLabelText('Port 1'), '8080:80');
    expect(await screen.findByText('Port 8080 is also published by another service in this stack')).toBeTruthy();
  });

  it('the note is protocol-aware and ignores the host ip', async () => {
    const user = await mount({ 'GET /api/containers': { body: [row('dns', { stack: 'dns', ports: ['127.0.0.1:53->53/udp'] })] } });
    await addPort(user);
    await user.type(screen.getByLabelText('Port 1'), '53:53');
    expect(await screen.findByText('Port 53 is free')).toBeTruthy();
    await user.type(screen.getByLabelText('Port 1'), '/udp');
    expect(await screen.findByText('Port 53 is used by dns')).toBeTruthy();
  });

  it('the check follows a container list refresh', async () => {
    let rows = [row('ha', { stack: 'home-assistant', ports: [] })];
    const user = await mount({ 'GET /api/containers': () => ({ body: rows }) });
    await addPort(user);
    await user.type(screen.getByLabelText('Port 1'), '8123:8123');
    expect(await screen.findByText('Port 8123 is free')).toBeTruthy();
    rows = [HOME_ASSISTANT];
    await emitChange({ project: '', seq: 1, resync: true });
    expect(await screen.findByText('Port 8123 is used by home-assistant')).toBeTruthy();
  });
});

describe('New stack compose preview', () => {
  it('shows every card in order and matches the posted body', async () => {
    const user = await mount();
    await fillImage(user);
    await addPort(user);
    await user.type(screen.getByLabelText('Port 1'), '8080:80');
    await user.click(screen.getByRole('button', { name: '+ Add another service' }));
    await user.type(screen.getByLabelText('Service name'), 'db');
    await user.type(screen.getByLabelText('Image'), 'postgres:17');
    expect(preview().textContent).toBe('services:\n  web:\n    image: nginx:1.27\n    ports:\n      - "8080:80"\n  db:\n    image: postgres:17\n');
    await submit(user);
    await waitFor(() => expect(post()).toBeDefined());
    expect((post()?.body as { services: { service: string; config: unknown }[] }).services).toEqual([
      { service: 'web', config: { image: 'nginx:1.27', ports: ['8080:80'], limits: {} } },
      { service: 'db', config: { image: 'postgres:17', limits: {} } },
    ]);
  });

  it('updates as the stack name and source change', async () => {
    const user = await mount();
    await user.type(screen.getByLabelText('Stack name'), 'app');
    expect(preview().textContent).toContain('  app:');
    await user.click(screen.getByLabelText('From Existing build'));
    await user.type(screen.getByLabelText('Build context'), '/srv/app');
    expect(preview().textContent).toContain('build: /srv/app');
  });

  it('starts as an empty service', async () => {
    await mount();
    expect(preview().textContent).toBe('services:\n  service-1: {}\n');
    expect(screen.getByText('my-stack')).toBeTruthy();
    expect(screen.getByText('new file')).toBeTruthy();
  });
});

describe('New stack unsaved-changes guard', () => {
  it('marks the page dirty once something is typed and clean again on unmount', async () => {
    const user = await mount();
    expect(get(unsavedChanges)).toBe(false);
    await user.type(screen.getByLabelText('Stack name'), 'w');
    expect(get(unsavedChanges)).toBe(true);
    mounted?.unmount();
    expect(get(unsavedChanges)).toBe(false);
  });

  it('clears the unsaved flag before navigating after a successful create', async () => {
    const user = await mount();
    const seen: boolean[] = [];
    const probe = screen.getByTestId('location');
    const observer = new MutationObserver(() => {
      if (probe.textContent?.startsWith('/stacks/web') && seen.length === 0) seen.push(get(unsavedChanges));
    });
    observer.observe(probe, { childList: true, characterData: true, subtree: true });
    await fillImage(user);
    expect(get(unsavedChanges)).toBe(true);
    await submit(user);
    await waitFor(() => expect(location()).toBe('/stacks/web?tab=operations&op=op1'));
    observer.disconnect();
    expect(seen).toEqual([false]);
    expect(get(unsavedChanges)).toBe(false);
  });

  it('the Cancel link asks before leaving a dirty form', async () => {
    routes({
      'GET /api/status': { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
      'GET /api/me': { body: { username: 'admin' } },
      'GET /api/setup/status': { body: { needsSetup: false } },
      'GET /api/host': { body: {} },
    });
    const stream = installFakeStream({ '/api/events': [] });
    try {
      window.history.replaceState(null, '', '/stacks/new');
      render(<App />);
      const user = userEvent.setup();
      await user.type(await screen.findByLabelText('Stack name'), 'w');
      await user.click(screen.getByRole('link', { name: 'Cancel' }));
      const dialog = await screen.findByRole('alertdialog', { name: 'Discard unsaved changes?' });
      expect(window.location.pathname).toBe('/stacks/new');
      await user.click(within(dialog).getByRole('button', { name: 'Discard changes' }));
      await waitFor(() => expect(window.location.pathname).toBe('/'));
      expect(get(unsavedChanges)).toBe(false);
    } finally {
      stream.restore();
    }
  });
});
