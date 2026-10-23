import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import type { PushTarget, RegistryView } from '$lib/api/imageTypes';
import { imageDetail, installImagesBackend, mountImages, version, type ImagesHarness } from './imagesTestKit';

let backend: ImagesHarness;
afterEach(() => backend.restore());

const IMG = '/api/images/web';
const REG: RegistryView = {
  id: 'r1', name: 'Docker Hub', kind: 'dockerhub', host: 'docker.io', username: 'pavel', region: '', mode: '', profile: '',
  accountId: '', configured: true, lastTestOk: true, lastTestMessage: '',
};
const TARGET: PushTarget = { id: 't1', registryId: 'r1', registryName: 'Docker Hub', kind: 'dockerhub', repository: 'pavel/web', autoPush: false };

function install(over: Parameters<typeof imageDetail>[0], extra: Parameters<typeof installImagesBackend>[0] = {}) {
  backend = installImagesBackend({
    [`GET ${IMG}`]: { body: imageDetail(over) },
    'GET /api/images/registries': { body: [REG] },
    ...extra,
  });
}

describe('Push targets card', () => {
  it('adds a target', async () => {
    install({}, { [`POST ${IMG}/push-targets`]: { status: 201, body: TARGET } });
    mountImages('/images/web?tab=settings');
    await userEvent.click(await screen.findByRole('button', { name: 'Add push target' }));
    const dialog = await screen.findByRole('dialog');
    await userEvent.selectOptions(await within(dialog).findByLabelText('Registry'), 'r1');
    await userEvent.type(within(dialog).getByLabelText('Repository'), 'pavel/web');
    await userEvent.click(within(dialog).getByLabelText(/Push automatically/));
    await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(backend.calls('POST', `${IMG}/push-targets`)).toHaveLength(1));
    expect(backend.calls('POST', `${IMG}/push-targets`)[0].body).toEqual({ registryId: 'r1', repository: 'pavel/web', autoPush: true });
  });

  it('lists targets and removes one after confirming', async () => {
    install({ pushTargets: [TARGET] }, { [`DELETE ${IMG}/push-targets/t1`]: { status: 204 } });
    mountImages('/images/web?tab=settings');
    expect(await screen.findByText('pavel/web')).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'Remove pavel/web' }));
    await userEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Remove target' }));
    await waitFor(() => expect(backend.calls('DELETE', `${IMG}/push-targets/t1`)).toHaveLength(1));
  });
});

describe('Versions tab push', () => {
  const pushed = version({ pushes: [{ target: 't1', registry: 'Docker Hub', repository: 'pavel/web', status: 'pushed', digest: 'sha256:x', error: '', updatedAt: '2026-10-20T10:02:00Z' }] });

  it('shows a status chip per target', async () => {
    install({ pushTargets: [TARGET], versions: [pushed] });
    mountImages('/images/web?tab=versions');
    expect(await screen.findByText('Docker Hub: pushed')).toBeTruthy();
  });

  it('shows failed pushes with the error', async () => {
    const failed = version({ pushes: [{ ...pushed.pushes[0], status: 'failed', error: 'denied' }] });
    install({ pushTargets: [TARGET], versions: [failed] });
    mountImages('/images/web?tab=versions');
    const chip = await screen.findByText('Docker Hub: failed');
    expect(chip.getAttribute('title')).toBe('denied');
  });

  it('pushes by tag with the target id and shows the operation', async () => {
    install(
      { pushTargets: [TARGET], versions: [version()] },
      { [`POST ${IMG}/versions/3/push`]: { status: 202, body: { id: 'op-b1', stack: 'image/abcdef012345/web', action: 'push', state: 'running', startedAt: '2026-10-23T10:00:00Z' } } },
    );
    mountImages('/images/web?tab=versions');
    await userEvent.click(await screen.findByRole('button', { name: 'Push 3 to Docker Hub pavel/web' }));
    await waitFor(() => expect(backend.calls('POST', `${IMG}/versions/3/push`)).toHaveLength(1));
    expect(backend.calls('POST', `${IMG}/versions/3/push`)[0].body).toEqual({ target: 't1' });
    expect(await screen.findByRole('region', { name: 'Operation output' })).toBeTruthy();
  });

  it('offers no Push for a failed version', async () => {
    install({ pushTargets: [TARGET], versions: [version({ status: 'failed' })] });
    mountImages('/images/web?tab=versions');
    await screen.findByRole('row', { name: /^3\b/ });
    expect(screen.queryByRole('button', { name: /^Push 3/ })).toBeNull();
  });
});
