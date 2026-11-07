import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { image, installImagesBackend, localOwner, mountImages, peerOwner, version, type ImagesHarness } from './imagesTestKit';

let backend: ImagesHarness;
afterEach(() => backend.restore());

const LIST = '/api/images';

describe('Images list', () => {
  it('shows name, source, owner, latest tag, last build and push target count on each card', async () => {
    backend = installImagesBackend({
      [`GET ${LIST}`]: {
        body: {
          servers: [localOwner],
          images: [
            image({
              latestVersion: version({ tag: '7' }),
              lastBuild: version({ tag: '7', status: 'failed' }),
              pushTargets: [
                { id: 't1', registryId: 'r1', registryName: 'Hub', kind: 'hub', repository: 'me/web', autoPush: false },
                { id: 't2', registryId: 'r2', registryName: 'ECR', kind: 'ecr', repository: 'web', autoPush: true },
              ],
            }),
          ],
        },
      },
    });
    mountImages('/images');
    const card = (await screen.findByRole('link', { name: 'Web' })).closest('li') as HTMLElement;
    expect(within(card).getByText('upload')).toBeTruthy();
    expect(within(card).getByText('Here')).toBeTruthy();
    expect(within(card).getByText('7')).toBeTruthy();
    expect(within(card).getByText('Push targets').nextElementSibling?.textContent).toBe('2');
    expect(screen.getByRole('link', { name: 'Web' }).getAttribute('href')).toBe('/images/web');
  });

  it('card shows a failed build', async () => {
    backend = installImagesBackend({
      [`GET ${LIST}`]: { body: { servers: [localOwner], images: [image({ latestVersion: version({ tag: '6' }), lastBuild: version({ tag: '7', status: 'failed' }) })] } },
    });
    mountImages('/images');
    const card = (await screen.findByRole('link', { name: 'Web' })).closest('li') as HTMLElement;
    expect(within(card).getByText('failed').getAttribute('data-tone')).toBe('danger');
  });

  it('explains that the library is separate from images on disk and links to Storage', async () => {
    backend = installImagesBackend({ [`GET ${LIST}`]: { body: { servers: [localOwner], images: [] } } });
    mountImages('/images');
    expect(await screen.findByText(/Images you build and version here/)).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Storage' }).getAttribute('href')).toBe('/storage');
  });

  it('links a peer-owned image to the owner route', async () => {
    backend = installImagesBackend({
      [`GET ${LIST}`]: { body: { servers: [localOwner, peerOwner], images: [image({ slug: 'base', name: 'Base', owner: peerOwner })] } },
    });
    mountImages('/images');
    const link = await screen.findByRole('link', { name: 'Base' });
    expect(link.getAttribute('href')).toBe('/s/mini-02/images/base');
  });

  it('shows an owner chip on peer-owned cards only and no card actions', async () => {
    backend = installImagesBackend({
      [`GET ${LIST}`]: { body: { servers: [localOwner, peerOwner], images: [image(), image({ slug: 'base', name: 'Base', owner: peerOwner })] } },
    });
    mountImages('/images');
    const peerCard = (await screen.findByRole('link', { name: 'Base' })).closest('li') as HTMLElement;
    expect(within(peerCard).getByText('Mini 02').getAttribute('data-tone')).toBe('info');
    expect(within(peerCard).queryByRole('button')).toBeNull();
    const localCard = screen.getByRole('link', { name: 'Web' }).closest('li') as HTMLElement;
    expect(within(localCard).getByText('Here').getAttribute('data-tone')).toBeNull();
  });

  it('warns about an unreachable server and one that runs an older Stackstead', async () => {
    const down = { ...peerOwner, status: 'unreachable' as const };
    const old = { id: 'peer-2', name: 'nas', slug: 'nas', local: false, status: 'old' as const };
    backend = installImagesBackend({ [`GET ${LIST}`]: { body: { servers: [localOwner, down, old, { ...peerOwner, id: 'peer-3', name: 'fine' }], images: [image()] } } });
    mountImages('/images');
    expect(await screen.findByText('Mini 02 could not be reached')).toBeTruthy();
    expect(screen.getByText('nas runs an older Stackstead without the image library')).toBeTruthy();
    expect(screen.queryByText(/fine/)).toBeNull();
  });

  it('shows an empty state when there are no images', async () => {
    backend = installImagesBackend({ [`GET ${LIST}`]: { body: { servers: [localOwner], images: [] } } });
    mountImages('/images');
    expect(await screen.findByText('No images yet')).toBeTruthy();
  });

  it('creates an upload image from the New image dialog and opens it', async () => {
    backend = installImagesBackend({
      [`GET ${LIST}`]: { body: { servers: [localOwner], images: [] } },
      [`POST ${LIST}`]: { body: image({ slug: 'my-app', name: 'My App' }) },
      'GET /api/images/my-app': { body: { ...image({ slug: 'my-app', name: 'My App' }), versions: [], suggestedTag: '1', tagHint: '', arch: 'amd64', revision: 1 } },
      'GET /api/images/my-app/dockerfile': { body: { content: 'FROM scratch\n', revision: 1 } },
      'GET /api/images/my-app/files': { body: { revision: 1, files: [] } },
    });
    mountImages('/images');
    await userEvent.click(await screen.findByRole('button', { name: 'New image' }));
    await userEvent.type(screen.getByLabelText('Name'), 'My App');
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    expect(await screen.findByRole('heading', { name: 'My App' })).toBeTruthy();
    const post = backend.calls('POST', LIST)[0];
    expect(post.body).toEqual({ name: 'My App', source: 'upload', dockerfilePath: 'Dockerfile', buildArgs: {} });
    expect(screen.getByTestId('location').textContent).toBe('/images/my-app');
  });

  it('shows the server message when creating fails', async () => {
    backend = installImagesBackend({
      [`GET ${LIST}`]: { body: { servers: [localOwner], images: [] } },
      [`POST ${LIST}`]: { status: 422, body: { error: 'name is required' } },
    });
    mountImages('/images');
    await userEvent.click(await screen.findByRole('button', { name: 'New image' }));
    await userEvent.type(screen.getByLabelText('Name'), 'x');
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    expect(await screen.findByText('name is required')).toBeTruthy();
  });
});
