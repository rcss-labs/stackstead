import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import FileBrowser from './FileBrowser.svelte';

let fake: FakeFetch;
afterEach(() => fake?.restore());

const dir = (name: string) => ({ name, isDir: true, size: 0 });
const file = (name: string, size = 10) => ({ name, isDir: false, size });

function setup(extra: Record<string, unknown> = {}, routes = {}) {
  fake = installFakeFetch({
    'GET /api/fs': { body: [dir('/srv/stacks'), dir('/home/me')] },
    'GET /api/fs?path=%2Fsrv%2Fstacks': { body: [file('notes.txt'), file('Dockerfile'), dir('web'), file('app.Dockerfile')] },
    'GET /api/fs?path=%2Fsrv%2Fstacks%2Fweb': { body: [file('index.html')] },
    ...routes,
  });
  const onpick = vi.fn();
  const onclose = vi.fn();
  render(FileBrowser, { open: true, mode: 'dir', title: 'Pick a directory', onpick, onclose, ...extra });
  return { onpick, onclose };
}

describe('FileBrowser', () => {
  it('lists the allowed roots first', async () => {
    setup();
    expect(await screen.findByRole('button', { name: '/srv/stacks' })).toBeTruthy();
    expect(screen.getByRole('button', { name: '/home/me' })).toBeTruthy();
    expect(screen.getByRole('dialog', { name: 'Pick a directory' })).toBeTruthy();
    expect(fake.calls[0].path).toBe('/api/fs');
  });

  it('entering a directory fetches ?path= and lists directories before files', async () => {
    setup({ mode: 'file' });
    await userEvent.click(await screen.findByRole('button', { name: '/srv/stacks' }));
    await screen.findByRole('button', { name: 'web' });
    expect(fake.calls.at(-1)!.path).toBe('/api/fs?path=%2Fsrv%2Fstacks');
    const names = screen.getAllByRole('listitem').map((li) => li.textContent?.trim());
    expect(names[0]).toContain('web');
  });

  it('breadcrumbs navigate back to a parent and to the root list', async () => {
    setup();
    await userEvent.click(await screen.findByRole('button', { name: '/srv/stacks' }));
    await userEvent.click(await screen.findByRole('button', { name: 'web' }));
    await screen.findByText('index.html');
    const nav = screen.getByRole('navigation', { name: 'Current location' });
    expect(nav.textContent).toContain('web');
    await userEvent.click(screen.getByRole('button', { name: '/srv/stacks', description: undefined }));
    await waitFor(() => expect(screen.queryByText('index.html')).toBeNull());
    expect(await screen.findByRole('button', { name: 'web' })).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'Allowed directories' }));
    expect(await screen.findByRole('button', { name: '/home/me' })).toBeTruthy();
  });

  it('dir mode returns the current directory', async () => {
    const { onpick } = setup();
    await userEvent.click(await screen.findByRole('button', { name: '/srv/stacks' }));
    await userEvent.click(await screen.findByRole('button', { name: 'web' }));
    await screen.findByText('index.html');
    await userEvent.click(screen.getByRole('button', { name: 'Use this directory' }));
    expect(onpick).toHaveBeenCalledWith('/srv/stacks/web');
  });

  it('dir mode has no pick button on the root list and does not offer files as picks', async () => {
    setup();
    await screen.findByRole('button', { name: '/srv/stacks' });
    expect(screen.queryByRole('button', { name: 'Use this directory' })).toBeNull();
  });

  it('file mode returns the clicked file and puts Dockerfiles first', async () => {
    const { onpick } = setup({ mode: 'file' });
    await userEvent.click(await screen.findByRole('button', { name: '/srv/stacks' }));
    await screen.findByRole('button', { name: 'app.Dockerfile' });
    const items = screen.getAllByRole('listitem').map((li) => li.textContent ?? '');
    const order = ['web', 'Dockerfile', 'app.Dockerfile', 'notes.txt'].map((n) => items.findIndex((t) => t.includes(n)));
    expect(order).toEqual([...order].sort((a, b) => a - b));
    expect(screen.queryByRole('button', { name: 'Use this directory' })).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: 'notes.txt' }));
    expect(onpick).toHaveBeenCalledWith('/srv/stacks/notes.txt');
  });

  it('honours a custom filter by listing matches first', async () => {
    setup({ mode: 'file', filter: (e: { name: string }) => e.name.endsWith('.txt') });
    await userEvent.click(await screen.findByRole('button', { name: '/srv/stacks' }));
    await screen.findByRole('button', { name: 'notes.txt' });
    const items = screen.getAllByRole('listitem').map((li) => li.textContent ?? '');
    expect(items.findIndex((t) => t.includes('notes.txt'))).toBeLessThan(items.findIndex((t) => t.includes('Dockerfile')));
  });

  it('403 outside the roots shows the allowed-directories message', async () => {
    setup({}, { 'GET /api/fs?path=%2Fsrv%2Fstacks': { status: 403, body: { error: 'path is outside the allowed directories' } } });
    await userEvent.click(await screen.findByRole('button', { name: '/srv/stacks' }));
    expect((await screen.findByRole('alert')).textContent).toContain('outside the allowed directories');
  });

  it('403 permission denied is distinguished from outside-roots', async () => {
    setup({}, { 'GET /api/fs?path=%2Fsrv%2Fstacks': { status: 403, body: { error: 'permission denied' } } });
    await userEvent.click(await screen.findByRole('button', { name: '/srv/stacks' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toMatch(/permission denied/i);
    expect(alert.textContent).not.toContain('outside the allowed');
  });

  it('404 says there is no such directory', async () => {
    setup({}, { 'GET /api/fs?path=%2Fsrv%2Fstacks': { status: 404, body: { error: 'no such file or directory' } } });
    await userEvent.click(await screen.findByRole('button', { name: '/srv/stacks' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/no such directory/i);
  });

  it('shows an empty-directory note', async () => {
    setup({}, { 'GET /api/fs?path=%2Fsrv%2Fstacks': { body: [] } });
    await userEvent.click(await screen.findByRole('button', { name: '/srv/stacks' }));
    expect(await screen.findByText('This directory is empty.')).toBeTruthy();
  });

  it('Cancel closes', async () => {
    const { onclose } = setup();
    await userEvent.click(await screen.findByRole('button', { name: 'Cancel' }));
    expect(onclose).toHaveBeenCalled();
  });

  it('keeps focus inside the dialog after entering a directory and after a breadcrumb click', async () => {
    setup();
    await userEvent.click(await screen.findByRole('button', { name: '/srv/stacks' }));
    await screen.findByRole('button', { name: 'web' });
    const dialog = screen.getByRole('dialog');
    await waitFor(() => expect(dialog.contains(document.activeElement) && document.activeElement !== dialog).toBe(true));
    await userEvent.click(screen.getByRole('button', { name: 'web' }));
    await screen.findByText('index.html');
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    expect(document.activeElement).not.toBe(document.body);
    expect(screen.getByRole('status').textContent).toContain('/srv/stacks/web');
    await userEvent.click(screen.getByRole('button', { name: '/srv/stacks' }));
    await screen.findByRole('button', { name: 'web' });
    await waitFor(() => expect(dialog.contains(document.activeElement)).toBe(true));
    expect(document.activeElement).not.toBe(document.body);
  });

  it('ignores a stale response that arrives after a newer navigation', async () => {
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    setup(
      {},
      {
        'GET /api/fs?path=%2Fsrv%2Fstacks': async () => {
          await gate;
          return { body: [dir('slow-child')] };
        },
        'GET /api/fs?path=%2Fhome%2Fme': { body: [dir('fast-child')] },
      },
    );
    await userEvent.click(await screen.findByRole('button', { name: '/srv/stacks' }));
    await userEvent.click(screen.getByRole('button', { name: '/home/me' }));
    await screen.findByRole('button', { name: 'fast-child' });
    release();
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByRole('button', { name: 'slow-child' })).toBeNull();
    expect(screen.getByRole('button', { name: 'fast-child' })).toBeTruthy();
  });

  it('handles an allowed root of "/" in joins and breadcrumbs', async () => {
    const { onpick } = setup(
      {},
      {
        'GET /api/fs': { body: [dir('/')] },
        'GET /api/fs?path=%2F': { body: [dir('etc')] },
        'GET /api/fs?path=%2Fetc': { body: [file('hosts')] },
      },
    );
    await userEvent.click(await screen.findByRole('button', { name: '/' }));
    await userEvent.click(await screen.findByRole('button', { name: 'etc' }));
    await screen.findByText('hosts');
    expect(fake.calls.at(-1)!.path).toBe('/api/fs?path=%2Fetc');
    expect(screen.getByRole('navigation', { name: 'Current location' }).textContent).not.toContain('//');
    await userEvent.click(screen.getByRole('button', { name: 'Use this directory' }));
    expect(onpick).toHaveBeenCalledWith('/etc');
  });

  it('lists duplicate roots without crashing', async () => {
    setup({}, { 'GET /api/fs': { body: [dir('/srv/stacks'), dir('/srv/stacks')] } });
    await screen.findAllByRole('button', { name: '/srv/stacks' });
    expect(screen.getAllByRole('button', { name: '/srv/stacks' })).toHaveLength(2);
  });

  it('disables file buttons in dir mode', async () => {
    setup();
    await userEvent.click(await screen.findByRole('button', { name: '/srv/stacks' }));
    const f = await screen.findByRole('button', { name: 'notes.txt' });
    expect((f as HTMLButtonElement).disabled).toBe(true);
  });
});
