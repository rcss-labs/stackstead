import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { filesOf, image, imageDetail, installImagesBackend, localOwner, mountImages, peerOwner, version, type ImagesHarness } from './imagesTestKit';

vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));

let backend: ImagesHarness;
afterEach(() => backend.restore());

const IMG = '/api/images/web';
const baseRoutes = (detail = imageDetail()) => ({
  [`GET ${IMG}`]: { body: detail },
  [`GET ${IMG}/dockerfile`]: { body: { content: 'FROM alpine\n', revision: 4 } },
  [`GET ${IMG}/files`]: { body: filesOf() },
  [`GET ${IMG}/revisions`]: { body: [] },
});

describe('Image detail', () => {
  it('selects the tab from ?tab=', async () => {
    backend = installImagesBackend(baseRoutes());
    mountImages('/images/web?tab=settings');
    const tab = await screen.findByRole('tab', { name: 'Settings' });
    expect(tab.getAttribute('aria-selected')).toBe('true');
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Source', 'Revisions', 'Versions', 'Settings']);
  });

  it('writes the tab to the URL when one is chosen', async () => {
    backend = installImagesBackend(baseRoutes());
    mountImages('/images/web?tab=source');
    await userEvent.click(await screen.findByRole('tab', { name: 'Versions' }));
    expect(screen.getByTestId('location').textContent).toBe('/images/web?tab=versions');
  });

  it('shows the latest version reference with a copy button', async () => {
    backend = installImagesBackend(baseRoutes());
    mountImages('/images/web?tab=versions');
    expect(await screen.findByText('stackstead.invalid/abcdef012345/web:3')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Copy' })).toBeTruthy();
  });

  it('opens the build dialog prefilled and starts a build that shows its operation', async () => {
    backend = installImagesBackend({
      ...baseRoutes(imageDetail({ suggestedTag: '4', buildArgs: { VERSION: '1' } })),
      [`POST ${IMG}/builds`]: {
        body: {
          version: version({ id: 'v-4', tag: '4', status: 'running' }),
          operation: { id: 'op-b1', stack: 'image/abcdef012345/web', action: 'build', state: 'running', startedAt: '2026-10-23T10:00:00Z' },
        },
      },
    });
    mountImages('/images/web?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: 'Build' }));
    const dialog = await screen.findByRole('dialog', { name: 'Build image' });
    expect((within(dialog).getByLabelText('Tag') as HTMLInputElement).value).toBe('4');
    expect((within(dialog).getByLabelText('Build arg 1 name') as HTMLInputElement).value).toBe('VERSION');
    expect((within(dialog).getByLabelText('Build arg 1 value') as HTMLInputElement).value).toBe('1');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Start build' }));
    expect(await screen.findByRole('region', { name: 'Operation output' })).toBeTruthy();
    expect(backend.calls('POST', `${IMG}/builds`)[0].body).toEqual({ tag: '4', buildArgs: { VERSION: '1' }, saveArgs: false, ref: '' });
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('shows a 409 build_running message inline and keeps the dialog open', async () => {
    backend = installImagesBackend({
      ...baseRoutes(),
      [`POST ${IMG}/builds`]: { status: 409, body: { error: 'a build is already running for this image', code: 'build_running' } },
    });
    mountImages('/images/web?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: 'Build' }));
    const dialog = await screen.findByRole('dialog', { name: 'Build image' });
    await userEvent.click(within(dialog).getByRole('button', { name: 'Start build' }));
    expect(await within(dialog).findByText('a build is already running for this image')).toBeTruthy();
    await waitFor(() => expect(screen.queryByRole('region', { name: 'Operation output' })).toBeNull());
  });

  it('shows not found for an unknown image', async () => {
    backend = installImagesBackend({ [`GET ${IMG}`]: { status: 404, body: { error: 'image not found' } } });
    mountImages('/images/web');
    expect(await screen.findByText('Image not found')).toBeTruthy();
  });
});

describe('Image owned by a peer', () => {
  const missing = { status: 404, body: { error: 'image not found' } };

  it('redirects a slug listed under a direct peer to the owner route', async () => {
    backend = installImagesBackend({
      [`GET ${IMG}`]: missing,
      'GET /api/images': { body: { servers: [localOwner, peerOwner], images: [image({ owner: peerOwner })] } },
    });
    mountImages('/images/web');
    expect(await screen.findByText('peer image')).toBeTruthy();
    expect(screen.getByTestId('location').textContent).toBe('/s/mini-02/images/web');
  });

  it('shows not found when the library lists the slug nowhere', async () => {
    backend = installImagesBackend({ [`GET ${IMG}`]: missing, 'GET /api/images': { body: { servers: [localOwner], images: [] } } });
    mountImages('/images/web');
    expect(await screen.findByText('Image not found')).toBeTruthy();
    expect(screen.getByTestId('location').textContent).toBe('/images/web');
  });

  it('shows not found when the slug is listed under this same owner', async () => {
    backend = installImagesBackend({ [`GET ${IMG}`]: missing, 'GET /api/images': { body: { servers: [localOwner], images: [image()] } } });
    mountImages('/images/web');
    expect(await screen.findByText('Image not found')).toBeTruthy();
    expect(screen.getByTestId('location').textContent).toBe('/images/web');
  });

  it('shows not found for an owner that is not a direct peer', async () => {
    backend = installImagesBackend({
      [`GET ${IMG}`]: missing,
      'GET /api/images': { body: { servers: [localOwner], images: [image({ owner: peerOwner })] } },
    });
    mountImages('/images/web');
    expect(await screen.findByText('Image not found')).toBeTruthy();
  });

  it('does not redirect a second time after a redirect', async () => {
    backend = installImagesBackend({
      [`GET ${IMG}`]: missing,
      'GET /api/images': { body: { servers: [localOwner, peerOwner], images: [image({ owner: peerOwner })] } },
    });
    mountImages({ pathname: '/images/web', state: { redirected: true } });
    expect(await screen.findByText('Image not found')).toBeTruthy();
    expect(screen.getByTestId('location').textContent).toBe('/images/web');
  });
});

describe('Image settings tab', () => {
  it('saves the build arg defaults and retention', async () => {
    backend = installImagesBackend({
      ...baseRoutes(imageDetail({ buildArgs: { A: '1' }, retention: { keepLast: 5, keepDays: 0 } })),
      [`PUT ${IMG}`]: { body: imageDetail() },
    });
    mountImages('/images/web?tab=settings');
    const keepDays = await screen.findByLabelText('Keep days');
    await userEvent.clear(keepDays);
    await userEvent.type(keepDays, '30');
    await userEvent.clear(screen.getByLabelText('Build arg 1 value'));
    await userEvent.type(screen.getByLabelText('Build arg 1 value'), '2');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(backend.calls('PUT', IMG)).toHaveLength(1));
    expect(backend.calls('PUT', IMG)[0].body).toEqual({
      name: 'Web',
      dockerfilePath: 'Dockerfile',
      buildArgs: { A: '2' },
      retention: { keepLast: 5, keepDays: 30 },
    });
  });

  it('deletes the image after confirmation and returns to the list', async () => {
    backend = installImagesBackend({
      ...baseRoutes(),
      [`DELETE ${IMG}`]: { status: 204 },
      'GET /api/images': { body: { servers: [], images: [] } },
    });
    mountImages('/images/web?tab=settings');
    await userEvent.click(await screen.findByRole('button', { name: 'Delete image' }));
    const dialog = await screen.findByRole('alertdialog');
    expect(backend.calls('DELETE', IMG)).toHaveLength(0);
    await userEvent.click(within(dialog).getByRole('button', { name: 'Delete image' }));
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/images'));
    expect(backend.calls('DELETE', IMG)).toHaveLength(1);
  });
});
