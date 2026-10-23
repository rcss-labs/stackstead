import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { imageDetail, installImagesBackend, mountImages, version, type ImagesHarness } from './imagesTestKit';

let backend: ImagesHarness;
afterEach(() => backend.restore());

const IMG = '/api/images/web';
const USED = [
  { server: 'local', serverName: 'Here', stack: 'web', service: 'app' },
  { server: 'local', serverName: 'Here', stack: 'blog', service: 'site' },
];
const v3 = version({ tag: '3', buildArgs: { VERSION: '1' }, revision: 4 });
const v2 = version({ id: 'v-2', tag: '2', status: 'failed', commit: 'abcdef1234567', revision: 3, arch: 'arm64', buildArgs: {} });

function install(extra: Parameters<typeof installImagesBackend>[0] = {}) {
  backend = installImagesBackend({
    [`GET ${IMG}`]: { body: imageDetail({ versions: [v3, v2] }) },
    [`GET ${IMG}/versions/3`]: { body: { ...v3, log: 'step 1\nstep 2', usedBy: USED } },
    [`GET ${IMG}/versions/2`]: { body: { ...v2, log: 'boom', usedBy: [] } },
    ...extra,
  });
}

describe('Versions tab', () => {
  it('shows tag, revision or commit, arch, build args, status and used-by count', async () => {
    install();
    mountImages('/images/web?tab=versions');
    const row3 = (await screen.findByRole('row', { name: /^3\b/ })) as HTMLElement;
    await waitFor(() => expect(within(row3).getAllByRole('cell').map((c) => c.textContent)).toEqual(['3', 'r4', 'amd64', 'VERSION=1', 'succeeded', '2', 'OpenDelete']));
    const row2 = screen.getByRole('row', { name: /^2\b/ });
    const cells = within(row2).getAllByRole('cell').map((c) => c.textContent);
    expect(cells.slice(0, 6)).toEqual(['2', 'abcdef1', 'arm64', '—', 'failed', '0']);
    expect(screen.getByText(/not secrets/i)).toBeTruthy();
  });

  it('opens a version to show its build log', async () => {
    install();
    mountImages('/images/web?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: 'Open 3' }));
    const log = await screen.findByRole('log', { name: 'Build log for 3' });
    expect(log.textContent).toBe('step 1\nstep 2');
  });

  it('asks before deleting a version and deletes it', async () => {
    install({ [`DELETE ${IMG}/versions/2`]: { status: 204 } });
    mountImages('/images/web?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: 'Delete 2' }));
    const dialog = await screen.findByRole('alertdialog');
    expect(backend.calls('DELETE', `${IMG}/versions/2`)).toHaveLength(0);
    await userEvent.click(within(dialog).getByRole('button', { name: 'Delete version' }));
    await waitFor(() => expect(backend.calls('DELETE', `${IMG}/versions/2`)).toHaveLength(1));
  });

  it('lists the services using a version when the delete is refused with image_in_use', async () => {
    install({ [`DELETE ${IMG}/versions/3`]: { status: 409, body: { error: 'image is in use', code: 'image_in_use', usedBy: USED } } });
    mountImages('/images/web?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: 'Delete 3' }));
    await userEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete version' }));
    const banner = await screen.findByRole('alert');
    expect(banner.textContent).toContain('image is in use');
    expect(within(banner).getByText('web / app')).toBeTruthy();
    expect(within(banner).getByText('blog / site')).toBeTruthy();
  });
});
