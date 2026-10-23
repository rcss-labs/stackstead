import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { filesOf, image, imageDetail, installImagesBackend, localOwner, mountImages, version, type ImagesHarness } from './imagesTestKit';

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

const IMG = '/api/images/gh';
const HEAD = { commit: 'abcdef1234567890abcdef1234567890abcdef12', dockerfile: 'FROM node:22\n', path: 'svc/Dockerfile' };
const ghDetail = () =>
  imageDetail({
    slug: 'gh',
    name: 'Gh',
    source: 'github',
    github: { repo: 'acme/app', branch: 'main', path: 'svc' },
    pushTargets: [{ id: 't1', registryId: 'r1', registryName: 'Hub', kind: 'dockerhub', repository: 'me/gh', autoPush: true }],
  });
const ghRoutes = () => ({
  [`GET ${IMG}`]: { body: ghDetail() },
  [`GET ${IMG}/github/head`]: { body: HEAD },
});

describe('GitHub images', () => {
  it('creates a GitHub image with repo, branch and path', async () => {
    backend = installImagesBackend({
      'GET /api/images': { body: { servers: [localOwner], images: [] } },
      'POST /api/images': { body: image({ slug: 'gh', name: 'Gh', source: 'github' }) },
      ...ghRoutes(),
    });
    mountImages('/images');
    await userEvent.click(await screen.findByRole('button', { name: 'New image' }));
    await userEvent.type(screen.getByLabelText('Name'), 'Gh');
    await userEvent.selectOptions(screen.getByLabelText('Source'), 'github');
    await userEvent.type(screen.getByLabelText('Repository'), 'acme/app');
    await userEvent.type(screen.getByLabelText('Path'), 'svc');
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(backend.calls('POST', '/api/images')).toHaveLength(1));
    expect(backend.calls('POST', '/api/images')[0].body).toEqual({
      name: 'Gh',
      source: 'github',
      github: { repo: 'acme/app', branch: 'main', path: 'svc' },
      dockerfilePath: 'Dockerfile',
      buildArgs: {},
    });
  });

  it('hides Revisions and shows the read-only Dockerfile at the branch head with an Open on GitHub link', async () => {
    backend = installImagesBackend(ghRoutes());
    mountImages('/images/gh?tab=source');
    expect(await screen.findByText(/FROM node:22/)).toBeTruthy();
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Source', 'Versions', 'Settings']);
    expect(screen.getByText(/abcdef1/)).toBeTruthy();
    expect(screen.queryByRole('textbox', { name: 'Dockerfile' })).toBeNull();
    expect(screen.queryByText('Upload files')).toBeNull();
    const link = screen.getByRole('link', { name: 'Open on GitHub' });
    expect(link.getAttribute('href')).toBe(`https://github.com/acme/app/blob/${HEAD.commit}/svc/Dockerfile`);
    expect(screen.getByText('acme/app')).toBeTruthy();
  });

  it('falls back to the Source tab when ?tab=revisions is requested', async () => {
    backend = installImagesBackend(ghRoutes());
    mountImages('/images/gh?tab=revisions');
    expect((await screen.findByRole('tab', { name: 'Source' })).getAttribute('aria-selected')).toBe('true');
  });

  it('notes that GitHub images are pushed manually and never shows auto-push as on', async () => {
    backend = installImagesBackend(ghRoutes());
    mountImages('/images/gh?tab=settings');
    expect(await screen.findByText('GitHub images are pushed manually')).toBeTruthy();
    expect(screen.queryByText('Auto-push on')).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: 'Add push target' }));
    expect(within(await screen.findByRole('dialog')).queryByLabelText(/Push automatically/)).toBeNull();
  });

  it('keeps auto-push on when editing an existing GitHub target', async () => {
    backend = installImagesBackend({
      ...ghRoutes(),
      'GET /api/images/registries': { body: [{ id: 'r1', name: 'Hub', kind: 'dockerhub', host: 'docker.io' }] },
      [`PUT ${IMG}/push-targets/t1`]: { body: {} },
    });
    mountImages('/images/gh?tab=settings');
    await userEvent.click(await screen.findByRole('button', { name: 'Edit me/gh' }));
    const dialog = within(await screen.findByRole('dialog'));
    await waitFor(() => expect(dialog.getByRole('option', { name: 'Hub' })).toBeTruthy());
    await userEvent.click(dialog.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(backend.calls('PUT', `${IMG}/push-targets/t1`)).toHaveLength(1));
    expect((backend.calls('PUT', `${IMG}/push-targets/t1`)[0].body as { autoPush: boolean }).autoPush).toBe(true);
  });

  it('points to Settings when GitHub refuses the token, and drops the stale Dockerfile', async () => {
    backend = installImagesBackend({
      [`GET ${IMG}`]: { body: ghDetail() },
      [`GET ${IMG}/github/head`]: { status: 401, body: { error: 'bad credentials' } },
    });
    mountImages('/images/gh?tab=source');
    expect(await screen.findByText(/bad credentials/)).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Settings → GitHub' }).getAttribute('href')).toBe('/settings#settings-github');
    expect(screen.queryByLabelText('Dockerfile at branch head')).toBeNull();
  });

  async function openBuild() {
    backend = installImagesBackend({ ...ghRoutes(), [`POST ${IMG}/builds`]: { body: { version: version(), operation: { id: 'op-b1' } } } });
    mountImages('/images/gh?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: 'Build' }));
    return within(await screen.findByRole('dialog'));
  }
  const sent = () => backend.calls('POST', `${IMG}/builds`)[0].body as { ref: string };

  it('builds the branch head without a ref', async () => {
    const dialog = await openBuild();
    expect(dialog.getByRole('button', { name: 'Branch head' }).getAttribute('aria-pressed')).toBe('true');
    expect(dialog.queryByLabelText('Commit')).toBeNull();
    await userEvent.click(dialog.getByRole('button', { name: 'Start build' }));
    await waitFor(() => expect(backend.calls('POST', `${IMG}/builds`)).toHaveLength(1));
    expect(sent().ref).toBe('');
  });

  it('sends ref only when Commit is chosen, and validates 7 to 40 hex characters', async () => {
    const dialog = await openBuild();
    await userEvent.click(dialog.getByRole('button', { name: 'Commit' }));
    const commit = dialog.getByLabelText('Commit');
    await userEvent.type(commit, 'xyz');
    expect(dialog.getAllByText(/7 to 40 hex/).length).toBeGreaterThan(0);
    await userEvent.click(dialog.getByRole('button', { name: 'Start build' }));
    expect(backend.calls('POST', `${IMG}/builds`)).toHaveLength(0);
    await userEvent.clear(commit);
    await userEvent.type(commit, 'ABCDEF1');
    await userEvent.click(dialog.getByRole('button', { name: 'Start build' }));
    await waitFor(() => expect(backend.calls('POST', `${IMG}/builds`)).toHaveLength(1));
    expect(sent().ref).toBe('ABCDEF1');
  });

  it('drops the commit when switching back to Branch head', async () => {
    const dialog = await openBuild();
    await userEvent.click(dialog.getByRole('button', { name: 'Commit' }));
    await userEvent.type(dialog.getByLabelText('Commit'), 'abcdef1');
    await userEvent.click(dialog.getByRole('button', { name: 'Branch head' }));
    await userEvent.click(dialog.getByRole('button', { name: 'Start build' }));
    await waitFor(() => expect(backend.calls('POST', `${IMG}/builds`)).toHaveLength(1));
    expect(sent().ref).toBe('');
  });
});

describe('upload images are unchanged', () => {
  it('keeps the Revisions tab', async () => {
    backend = installImagesBackend({
      'GET /api/images/web': { body: imageDetail() },
      'GET /api/images/web/dockerfile': { body: { content: 'FROM a\n', revision: 4 } },
      'GET /api/images/web/files': { body: filesOf() },
    });
    mountImages('/images/web');
    await screen.findByRole('tab', { name: 'Revisions' });
  });
});
