import { cleanup, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { toasts } from '$lib/stores/toasts';
import { findRoute } from '../routes';
import { fake, location, mountAt, routes as baseRoutes } from './createTestKit';

vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const store = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  const changes = writable<{ project: string; seq: number; resync?: true } | null>(null);
  return {
    ...(await orig<typeof import('$lib/stores/live')>()),
    live: { subscribe: store.subscribe },
    changes: { subscribe: changes.subscribe },
    refreshStatus: vi.fn(),
  };
});

const SUMMARY = { name: 'blog', path: '/srv/blog/compose.yaml', project: 'blog', origin: 'external', status: 'stopped', serviceCount: 1, running: 0 };

/** The previous UI's fixture set: import POST, an allowed root /srv with a compose file and a non-yaml file. */
function routes(extra: Record<string, unknown> = {}) {
  return baseRoutes({
    'POST /api/stacks/import': { status: 201, body: SUMMARY },
    'GET /api/fs': { body: [{ name: '/srv', isDir: true, size: 0 }] },
    'GET /api/fs?path=%2Fsrv': { body: [{ name: 'notes.txt', isDir: false, size: 5 }, { name: 'compose.yaml', isDir: false, size: 5 }] },
    ...extra,
  });
}

async function mount(extra: Record<string, unknown> = {}) {
  routes(extra);
  mountAt('/stacks/import');
  await screen.findByLabelText('Compose file path'); // the screen is a lazy chunk
  return userEvent.setup();
}

const submit = (user: ReturnType<typeof userEvent.setup>) => user.click(screen.getByRole('button', { name: 'Import stack' }));
const importCall = () => fake.calls.find((c) => c.path === '/api/stacks/import');

afterEach(() => {
  cleanup();
  fake?.restore();
  toasts.reset();
  vi.unstubAllGlobals();
});

describe('Import stack screen', () => {
  it('explains the file is not modified', async () => {
    await mount();
    expect(screen.getByRole('heading', { name: 'Import stack' })).toBeTruthy();
    expect(screen.getByText(/Nothing is moved or changed until you edit it/)).toBeTruthy();
  });

  it('posts the path and navigates to the stack', async () => {
    const user = await mount();
    await user.type(screen.getByLabelText('Compose file path'), '/srv/blog/compose.yaml');
    await submit(user);
    await waitFor(() => expect(location()).toBe('/stacks/blog'));
    expect(importCall()?.body).toEqual({ path: '/srv/blog/compose.yaml' });
  });

  it('requires a path and focuses the field', async () => {
    const user = await mount();
    await submit(user);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Compose file path')));
    expect(screen.getByText('Enter the absolute path of a compose file.')).toBeTruthy();
    expect(importCall()).toBeUndefined();
  });

  for (const [status, message] of [
    [404, 'no such file or directory'],
    [409, 'stack already registered; override file present'],
    [403, 'permission denied'],
  ] as const) {
    it(`${status} shows the server message under the path`, async () => {
      const user = await mount({ 'POST /api/stacks/import': { status, body: { error: message } } });
      await user.type(screen.getByLabelText('Compose file path'), '/srv/x.yaml');
      await submit(user);
      expect(await screen.findByText(message)).toBeTruthy();
      await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Compose file path')));
      expect(location()).toBe('/stacks/import');
    });
  }

  it('400 with a path field error shows it under the path', async () => {
    const user = await mount({ 'POST /api/stacks/import': { status: 400, body: { error: 'invalid', fields: [{ field: 'path', message: 'file does not exist' }] } } });
    await user.type(screen.getByLabelText('Compose file path'), '/srv/x.yaml');
    await submit(user);
    expect(await screen.findByText('file does not exist')).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Compose file path')));
  });

  it('Cancel is a router link', async () => {
    const user = await mount();
    const cancel = screen.getByRole('link', { name: 'Cancel' });
    expect(cancel.getAttribute('href')).toBe('/');
    await user.click(cancel);
    await waitFor(() => expect(location()).toBe('/'));
  });

  it('422 shows the compose error verbatim', async () => {
    const user = await mount({ 'POST /api/stacks/import': { status: 422, body: { error: 'yaml: line 3\n  bad' } } });
    await user.type(screen.getByLabelText('Compose file path'), '/srv/x.yaml');
    await submit(user);
    const message = await screen.findByText(/yaml: line 3/);
    expect(message.textContent).toBe('yaml: line 3\n  bad');
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('alert')));
  });

  it('import error renders in the compose-file box with its message', async () => {
    const user = await mount({ 'POST /api/stacks/import': { status: 422, body: { error: 'yaml: line 3\n  bad' } } });
    await user.type(screen.getByLabelText('Compose file path'), '/srv/x.yaml');
    await submit(user);
    const box = await screen.findByRole('alert');
    expect(within(box).getByText('Compose file')).toBeTruthy();
    expect(within(box).getByText('cannot import')).toBeTruthy();
    expect(within(box).getByText(/yaml: line 3/)).toBeTruthy();
  });

  it('browse picks a file into the input', async () => {
    const user = await mount();
    await user.click(screen.getByRole('button', { name: 'Browse for compose file' }));
    await user.click(await screen.findByRole('button', { name: '/srv' }));
    const files = (await screen.findAllByRole('button', { name: /\.(yaml|txt)/ })).map((b) => b.textContent ?? '');
    expect(files[0]).toMatch(/compose\.yaml/); // the yaml filter lists it before notes.txt
    await user.click(await screen.findByRole('button', { name: /^compose\.yaml/ }));
    await waitFor(() => expect((screen.getByLabelText('Compose file path') as HTMLInputElement).value).toBe('/srv/compose.yaml'));
  });

  it('a partial-checks response raises a warning toast', async () => {
    const user = await mount({ 'POST /api/stacks/import': { status: 201, body: SUMMARY, headers: { 'X-Stackstead-Checks': 'partial' } } });
    await user.type(screen.getByLabelText('Compose file path'), '/srv/blog/compose.yaml');
    await submit(user);
    await waitFor(() => expect(location()).toBe('/stacks/blog'));
    expect(get(toasts).some((t) => t.kind === 'warning')).toBe(true);
  });

  it('503 shows the unavailable banner and keeps the path clean', async () => {
    const user = await mount({ 'POST /api/stacks/import': { status: 503, body: { error: 'docker daemon is down' } } });
    await user.type(screen.getByLabelText('Compose file path'), '/srv/x.yaml');
    await submit(user);
    expect(await screen.findByText('Docker/compose unavailable: the stack was not imported')).toBeTruthy();
    expect(screen.getByText('docker daemon is down')).toBeTruthy();
    expect(location()).toBe('/stacks/import');
  });
});

describe('Import stack route and layout', () => {
  it('the route table titles /stacks/import as Import stack', () => {
    expect(findRoute('/stacks/import')?.title).toBe('Import stack');
  });
});
