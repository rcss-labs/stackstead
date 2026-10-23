import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { imageDetail, installImagesBackend, mountImages, version, type ImagesHarness } from './imagesTestKit';

let backend: ImagesHarness;
afterEach(() => backend.restore());

const IMG = '/api/images/web';
const USED = [
  { server: 'abcdef012345ffff', serverName: 'Here', stack: 'web', service: 'app' },
  { server: 'abcdef012345ffff', serverName: 'Here', stack: 'blog', service: 'site' },
];
const v3 = version({ tag: '3', buildArgs: { VERSION: '1' }, revision: 4 });
const v2 = version({ id: 'v-2', tag: '2', status: 'failed', commit: 'abcdef1234567', revision: 3, arch: 'arm64', buildArgs: {} });

function install(extra: Parameters<typeof installImagesBackend>[0] = {}) {
  backend = installImagesBackend({
    [`GET ${IMG}`]: { body: imageDetail({ versions: [v3, v2] }) },
    [`GET ${IMG}/versions/v-3`]: { body: { ...v3, log: 'step 1\nstep 2', usedBy: USED, unreachable: [] } },
    [`GET ${IMG}/versions/v-2`]: { body: { ...v2, log: 'boom', usedBy: [], unreachable: [] } },
    ...extra,
  });
}

describe('Versions tab', () => {
  it('shows tag, revision or commit, arch, build args and status, and fetches no detail until a row is opened', async () => {
    install();
    mountImages('/images/web?tab=versions');
    const row3 = (await screen.findByRole('row', { name: /^3\b/ })) as HTMLElement;
    expect(within(row3).getAllByRole('cell').map((c) => c.textContent)).toEqual(['3', 'r4', 'amd64', 'VERSION=1', 'succeeded', 'OpenDelete']);
    const row2 = screen.getByRole('row', { name: /^2\b/ });
    expect(within(row2).getAllByRole('cell').map((c) => c.textContent).slice(0, 5)).toEqual(['2', 'abcdef1', 'arm64', '—', 'failed']);
    expect(screen.getByText(/not secrets/i)).toBeTruthy();
    expect(backend.fake.calls.filter((c) => c.path.includes('/versions/'))).toHaveLength(0);
  });

  it('opens a version to show its build log and the services using it', async () => {
    install();
    mountImages('/images/web?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: /^Open 3 / }));
    const log = await screen.findByRole('log', { name: 'Build log for 3' });
    expect(log.textContent).toBe('step 1\nstep 2');
    expect(screen.getByText('Used by 2 services')).toBeTruthy();
    expect(screen.getByText('web / app')).toBeTruthy();
    expect(backend.calls('GET', `${IMG}/versions/v-3`)).toHaveLength(1);
  });

  it('shows a notice instead of "not used" when peers could not be asked', async () => {
    const down = { id: 'peer-9', name: 'Mini 09', slug: 'mini-09', local: false, status: 'unreachable' };
    install({ [`GET ${IMG}/versions/v-3`]: { body: { ...v3, log: 'step 1', usedBy: [], unreachable: [down] } } });
    mountImages('/images/web?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: /^Open 3 / }));
    await screen.findByRole('log', { name: 'Build log for 3' });
    expect(screen.getByText('could not ask Mini 09')).toBeTruthy();
    expect(screen.queryByText('Used by 0 services')).toBeNull();
  });

  it('shows an inline error when the version detail cannot be loaded', async () => {
    install({ [`GET ${IMG}/versions/v-3`]: { status: 500, body: { error: 'log unreadable' } } });
    mountImages('/images/web?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: /^Open 3 / }));
    expect(await screen.findByText(/log unreadable/)).toBeTruthy();
  });

  it('keeps versions that share a tag apart: open and delete act on one row by id', async () => {
    const failedA = version({ id: 'v-a', tag: '1', status: 'failed', revision: 1 });
    const failedB = version({ id: 'v-b', tag: '1', status: 'failed', revision: 2 });
    const live = version({ id: 'v-c', tag: '1', status: 'succeeded', revision: 3 });
    install({
      [`GET ${IMG}`]: { body: imageDetail({ versions: [failedA, failedB, live] }) },
      [`GET ${IMG}/versions/v-b`]: { body: { ...failedB, log: 'log of b', usedBy: [], unreachable: [] } },
      [`DELETE ${IMG}/versions/v-a`]: { status: 204 },
    });
    mountImages('/images/web?tab=versions');
    const opens = await screen.findAllByRole('button', { name: /^Open 1 / });
    expect(opens).toHaveLength(3);
    await userEvent.click(opens[1]);
    expect((await screen.findByRole('log')).textContent).toBe('log of b');
    expect(screen.getAllByRole('log')).toHaveLength(1);
    expect(opens.map((b) => b.getAttribute('aria-expanded'))).toEqual(['false', 'true', 'false']);
    expect(backend.fake.calls.filter((c) => c.path.includes('/versions/'))).toHaveLength(1);

    await userEvent.click(screen.getAllByRole('button', { name: /^Delete 1 / })[0]);
    await userEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete version' }));
    await waitFor(() => expect(backend.calls('DELETE', `${IMG}/versions/v-a`)).toHaveLength(1));
  });

  it('asks before deleting a version and deletes it', async () => {
    install({ [`DELETE ${IMG}/versions/v-2`]: { status: 204 } });
    mountImages('/images/web?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: /^Delete 2 / }));
    const dialog = await screen.findByRole('alertdialog');
    expect(backend.calls('DELETE', `${IMG}/versions/v-2`)).toHaveLength(0);
    await userEvent.click(within(dialog).getByRole('button', { name: 'Delete version' }));
    await waitFor(() => expect(backend.calls('DELETE', `${IMG}/versions/v-2`)).toHaveLength(1));
  });

  it('lists the services using a version when the delete is refused with image_in_use', async () => {
    install({ [`DELETE ${IMG}/versions/v-3`]: { status: 409, body: { error: 'image is in use', code: 'image_in_use', usedBy: USED } } });
    mountImages('/images/web?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: /^Delete 3 / }));
    await userEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete version' }));
    const banner = await screen.findByRole('alert');
    expect(banner.textContent).toContain('image is in use');
    expect(within(banner).getByText('web / app')).toBeTruthy();
    expect(within(banner).getByText('blog / site')).toBeTruthy();
  });

  it('names the other server for a remote use and the servers that could not be asked', async () => {
    const remote = { server: 'peer-1', serverName: 'Mini 02', stack: 'shop', service: 'api' };
    const down = { id: 'peer-9', name: 'Mini 09', slug: 'mini-09', local: false, status: 'unreachable' };
    install({
      [`DELETE ${IMG}/versions/v-3`]: { status: 409, body: { error: 'image is in use', code: 'image_in_use', usedBy: [USED[0], remote], unreachable: [down] } },
    });
    mountImages('/images/web?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: /^Delete 3 / }));
    await userEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete version' }));
    const banner = await screen.findByRole('alert');
    expect(within(banner).getByText('web / app')).toBeTruthy();
    expect(within(banner).getByText('shop / api on Mini 02')).toBeTruthy();
    expect(within(banner).getByText('could not ask Mini 09')).toBeTruthy();
  });
});
