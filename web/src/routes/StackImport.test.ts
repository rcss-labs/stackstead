import { render, screen, waitFor, within } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { navigate, route } from '../lib/router';
import { toasts } from '../lib/stores/toasts';
import { installFakeFetch, type FakeFetch } from '../vitest-setup';
import StackImport from './StackImport.svelte';

let fake: FakeFetch;
const SUMMARY = { name: 'blog', path: '/srv/blog/compose.yaml', project: 'blog', origin: 'external', status: 'stopped', serviceCount: 1, running: 0 };

function routes(extra: Record<string, unknown> = {}) {
  fake = installFakeFetch({
    'GET /api/stacks': { body: [] },
    'GET /api/operations?state=running': { body: [] },
    'POST /api/stacks/import': { status: 201, body: SUMMARY },
    'GET /api/fs': { body: [{ name: '/srv', isDir: true, size: 0 }] },
    'GET /api/fs?path=%2Fsrv': { body: [{ name: 'compose.yaml', isDir: false, size: 5 }, { name: 'notes.txt', isDir: false, size: 5 }] },
    ...(extra as object),
  } as never);
}

beforeEach(() => navigate('/stacks/import'));
afterEach(() => {
  fake?.restore();
  toasts.reset();
});

const submit = (user: ReturnType<typeof userEvent.setup>) => user.click(screen.getByRole('button', { name: 'Import stack' }));

describe('StackImport', () => {
  it('explains the file is not modified', () => {
    routes();
    render(StackImport);
    expect(screen.getByText(/Nothing is moved or changed until you edit it/)).toBeTruthy();
  });

  it('posts the path and navigates to the stack', async () => {
    routes();
    const user = userEvent.setup();
    render(StackImport);
    await user.type(screen.getByLabelText('Compose file path'), '/srv/blog/compose.yaml');
    await submit(user);
    await waitFor(() => expect(get(route).path).toBe('/stacks/blog'));
    expect(fake.calls.find((c) => c.path === '/api/stacks/import')?.body).toEqual({ path: '/srv/blog/compose.yaml' });
  });

  it('requires a path and focuses the field', async () => {
    routes();
    const user = userEvent.setup();
    render(StackImport);
    await submit(user);
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Compose file path')));
    expect(fake.calls.some((c) => c.path === '/api/stacks/import')).toBe(false);
  });

  for (const [status, message] of [
    [404, 'no such file or directory'],
    [409, 'stack already registered; override file present'],
    [403, 'permission denied'],
  ] as const) {
    it(`${status} shows the server message under the path`, async () => {
      routes({ 'POST /api/stacks/import': { status, body: { error: message } } });
      const user = userEvent.setup();
      render(StackImport);
      await user.type(screen.getByLabelText('Compose file path'), '/srv/x.yaml');
      await submit(user);
      expect(await screen.findByText(message)).toBeTruthy();
      await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Compose file path')));
      expect(get(route).path).toBe('/stacks/import');
    });
  }

  it('400 with a path field error shows it under the path', async () => {
    routes({ 'POST /api/stacks/import': { status: 400, body: { error: 'invalid', fields: [{ field: 'path', message: 'file does not exist' }] } } });
    const user = userEvent.setup();
    render(StackImport);
    await user.type(screen.getByLabelText('Compose file path'), '/srv/x.yaml');
    await submit(user);
    expect(await screen.findByText('file does not exist')).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Compose file path')));
  });

  it('Cancel is a router link', () => {
    routes();
    render(StackImport);
    const a = screen.getByRole('link', { name: 'Cancel' });
    a.addEventListener('click', (e) => e.preventDefault());
    a.click();
    expect(get(route).path).toBe('/');
  });

  it('422 shows the compose error verbatim', async () => {
    routes({ 'POST /api/stacks/import': { status: 422, body: { error: 'yaml: line 3\n  bad' } } });
    const user = userEvent.setup();
    render(StackImport);
    await user.type(screen.getByLabelText('Compose file path'), '/srv/x.yaml');
    await submit(user);
    const message = await screen.findByText(/yaml: line 3/);
    expect(message.textContent).toBe('yaml: line 3\n  bad');
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('alert')));
  });

  it('import error renders in the compose-file box with its message', async () => {
    routes({ 'POST /api/stacks/import': { status: 422, body: { error: 'yaml: line 3\n  bad' } } });
    const user = userEvent.setup();
    render(StackImport);
    await user.type(screen.getByLabelText('Compose file path'), '/srv/x.yaml');
    await submit(user);
    const box = await screen.findByRole('alert');
    expect(within(box).getByText('Compose file')).toBeTruthy();
    expect(within(box).getByText('cannot import')).toBeTruthy();
    expect(within(box).getByText(/yaml: line 3/)).toBeTruthy();
  });

  it('browse picks a file into the input', async () => {
    routes();
    const user = userEvent.setup();
    render(StackImport);
    await user.click(screen.getByRole('button', { name: 'Browse for compose file' }));
    await user.click(await screen.findByRole('button', { name: '/srv' }));
    await user.click(await screen.findByRole('button', { name: /^compose\.yaml/ }));
    await waitFor(() => expect((screen.getByLabelText('Compose file path') as HTMLInputElement).value).toBe('/srv/compose.yaml'));
  });

  it('a partial-checks response raises a warning toast', async () => {
    routes({ 'POST /api/stacks/import': { status: 201, body: SUMMARY, headers: { 'X-Stackstead-Checks': 'partial' } } });
    const user = userEvent.setup();
    render(StackImport);
    await user.type(screen.getByLabelText('Compose file path'), '/srv/blog/compose.yaml');
    await submit(user);
    await waitFor(() => expect(get(route).path).toBe('/stacks/blog'));
    expect(get(toasts).some((t) => t.kind === 'warning')).toBe(true);
  });
});
