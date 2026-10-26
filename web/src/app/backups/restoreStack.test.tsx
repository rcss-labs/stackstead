import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { BackupArchive, BackupManifest, BackupRestorePlan } from '$lib/api/backupTypes';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { BackupsScreen } from './BackupsScreen';

vi.mock('../detail/OperationPanel', () => ({
  OperationPanel: ({ operationId }: { operationId: string }) => <div>following {operationId}</div>,
}));

let fake: FakeFetch;
afterEach(() => fake.restore());

const archive: BackupArchive = { dest: 'local', name: 'a1.tar.gz', hostSlug: 'mine', createdAt: '2026-10-26T03:00:00Z', size: 1024 };

const manifest: BackupManifest = {
  format: 1,
  stacksteadVersion: '1.0.0',
  host: 'Mine',
  hostSlug: 'mine',
  instanceId: 'i',
  createdAt: '2026-10-26T03:00:00Z',
  status: 'success',
  networks: 0,
  items: [],
  stacks: ['web', 'db'].map((name) => ({
    name,
    composePath: `/s/${name}/compose.yaml`,
    origin: 'managed',
    project: name,
    wasRunning: true,
    networks: [],
    volumes: [{ key: `${name}-data`, kind: 'volume', name: `${name}-data`, target: '/data', bytes: 2048 }],
    images: ['nginx:1'],
    liveCopy: false,
  })),
};

const plan = (over: Partial<BackupRestorePlan> = {}): BackupRestorePlan => ({
  stack: 'web',
  targetName: 'web',
  targetDir: '/s/web',
  composePath: '/s/web/compose.yaml',
  project: 'web',
  origin: 'managed',
  overwrites: false,
  fileDiffs: [{ path: 'compose.yaml', old: 'a: 1', new: 'a: 2' }],
  volumes: [
    { key: 'web-data', kind: 'volume', name: 'web-data', target: '/data', bytes: 2048, skip: false, reason: '' },
    { key: 'web-bind', kind: 'bind', name: '/srv/x', target: '/x', bytes: 10, skip: true, reason: 'bind mounts are not restored' },
  ],
  images: ['nginx:1'],
  missingNetworks: [{ name: 'edge', driver: 'bridge' }],
  crossHost: false,
  warnings: [],
  ...over,
});

const manifestPath = '/api/backups/archives/local/a1.tar.gz/manifest';

function setup(routes: Parameters<typeof installFakeFetch>[0]) {
  fake = installFakeFetch({
    'GET /api/backups/archives': { body: [archive] },
    'GET /api/peers/self': { body: { name: 'Mine', slug: 'mine' } },
    [`GET ${manifestPath}`]: { body: manifest },
    ...routes,
  });
  render(
    <MemoryRouter>
      <BackupsScreen />
    </MemoryRouter>,
  );
}

async function openDialog() {
  const user = userEvent.setup();
  await screen.findByText('a1.tar.gz');
  await user.click(screen.getByRole('button', { name: 'Restore' }));
  return user;
}

async function preview(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'Preview restore' }));
}

describe('single-stack restore', () => {
  it('lists the stacks in the archive manifest', async () => {
    setup({});
    await openDialog();
    const picker = await screen.findByRole('combobox', { name: 'Stack' });
    expect(within(picker).getAllByRole('option').map((o) => o.textContent)).toEqual(['web', 'db']);
  });

  it('sends the selected parts in the plan request', async () => {
    setup({ 'POST /api/backups/restore/plan': { body: plan() } });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    await user.click(screen.getByRole('checkbox', { name: 'Images' }));
    await user.click(screen.getByRole('checkbox', { name: 'Volumes' }));
    await preview(user);
    await screen.findByText('Missing networks');
    const call = fake.calls.find((c) => c.path === '/api/backups/restore/plan');
    expect(call?.body).toEqual({
      archive: { dest: 'local', name: 'a1.tar.gz' },
      stack: 'web',
      newName: '',
      files: true,
      volumes: true,
      images: true,
      volumeKeys: [],
    });
  });

  it('shows volume sizes, skip reasons, missing networks and the replace warning', async () => {
    setup({ 'POST /api/backups/restore/plan': { body: plan({ crossHost: true }) } });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    await user.click(screen.getByRole('checkbox', { name: 'Volumes' }));
    await preview(user);
    expect(await screen.findByText(/volume contents are replaced/i)).toBeTruthy();
    expect(screen.getByText('bind mounts are not restored')).toBeTruthy();
    expect(screen.getByText('2.0 KiB')).toBeTruthy();
    expect(screen.getByText('edge')).toBeTruthy();
    expect(screen.getByText('Cross-host')).toBeTruthy();
    expect(screen.getByTestId('diff-view')).toBeTruthy();
  });

  it('requires confirmation before starting the restore', async () => {
    setup({
      'POST /api/backups/restore/plan': { body: plan({ overwrites: true }) },
      'POST /api/backups/restore': { status: 202, body: { operation: { id: 'op-9' } } },
    });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    await preview(user);
    await user.click(await screen.findByRole('button', { name: 'Restore stack…' }));
    expect(fake.calls.some((c) => c.path === '/api/backups/restore')).toBe(false);
    const confirm = await screen.findByRole('alertdialog');
    expect(within(confirm).getByText(/overwrites/i)).toBeTruthy();
    await user.click(within(confirm).getByRole('button', { name: 'Restore' }));
    await screen.findByText('following op-9');
    expect(fake.calls.filter((c) => c.path === '/api/backups/restore')).toHaveLength(1);
  });

  it('shows the archive_newer message', async () => {
    setup({
      'POST /api/backups/restore/plan': {
        status: 400,
        body: { error: 'archive is from a newer Stackstead version: archive 2.0, installed 1.0', code: 'archive_newer' },
      },
    });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    await preview(user);
    expect(await screen.findByText(/newer Stackstead version/)).toBeTruthy();
  });

  it('offers a new name on a clash and sends it', async () => {
    setup({ 'POST /api/backups/restore/plan': { body: plan({ overwrites: true }) } });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    expect(screen.queryByLabelText('Restore as new name')).toBeNull();
    await preview(user);
    const field = await screen.findByLabelText('Restore as new name');
    fireEvent.change(field, { target: { value: 'web2' } });
    await preview(user);
    await waitFor(() => expect(fake.calls.filter((c) => c.path === '/api/backups/restore/plan')).toHaveLength(2));
    expect(fake.calls.filter((c) => c.path === '/api/backups/restore/plan')[1].body).toMatchObject({ newName: 'web2' });
  });

  it('shows the server message and the new-name field when the target is occupied', async () => {
    setup({
      'POST /api/backups/restore/plan': {
        status: 409,
        body: { error: 'target directory is not empty and belongs to no stack of that name: /s/web', code: 'target_occupied' },
      },
    });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    await preview(user);
    expect(await screen.findByText(/belongs to no stack of that name/)).toBeTruthy();
    expect(screen.getByLabelText('Restore as new name')).toBeTruthy();
  });

  it('explains that a stack missing here needs its files restored', async () => {
    setup({
      'POST /api/backups/restore/plan': {
        status: 400,
        body: { error: 'the stack is not on this server', code: 'files_required' },
      },
    });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    await user.click(screen.getByRole('checkbox', { name: 'Files' }));
    await user.click(screen.getByRole('checkbox', { name: 'Volumes' }));
    await preview(user);
    expect(await screen.findByText(/does not exist on this server/i)).toBeTruthy();
  });

  it('hides the restore button when a part changes after the preview', async () => {
    setup({ 'POST /api/backups/restore/plan': { body: plan() } });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    await preview(user);
    await screen.findByRole('button', { name: 'Restore stack…' });
    await user.click(screen.getByRole('checkbox', { name: 'Images' }));
    expect(screen.queryByRole('button', { name: 'Restore stack…' })).toBeNull();
  });

  it('drops a plan that resolves after the choices changed', async () => {
    let release: () => void = () => undefined;
    const gate = new Promise<void>((resolve) => (release = resolve));
    setup({
      'POST /api/backups/restore/plan': async () => {
        await gate;
        return { body: plan() };
      },
    });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    await preview(user);
    await user.click(screen.getByRole('checkbox', { name: 'Images' }));
    release();
    await waitFor(() => expect(fake.calls.some((c) => c.path === '/api/backups/restore/plan')).toBe(true));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole('button', { name: 'Restore stack…' })).toBeNull();
    expect(screen.queryByRole('region', { name: 'Restore preview' })).toBeNull();
  });

  it('blocks the preview when Volumes is ticked but every volume is unticked', async () => {
    setup({ 'POST /api/backups/restore/plan': { body: plan() } });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    await user.click(screen.getByRole('checkbox', { name: 'Volumes' }));
    await user.click(screen.getByRole('checkbox', { name: /web-data/ }));
    const button = screen.getByRole('button', { name: 'Preview restore' });
    expect(button.getAttribute('aria-disabled')).toBe('true');
    expect(screen.getByText('Choose at least one volume, or untick Volumes').textContent).toBeTruthy();
    await user.click(button);
    expect(fake.calls.some((c) => c.path === '/api/backups/restore/plan')).toBe(false);
  });

  it('words the confirmation by what happens', async () => {
    setup({
      'POST /api/backups/restore/plan': { body: plan({ overwrites: true }) },
      'POST /api/backups/restore': { status: 202, body: { operation: { id: 'op-9' } } },
    });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    await user.click(screen.getByRole('checkbox', { name: 'Files' }));
    await user.click(screen.getByRole('checkbox', { name: 'Volumes' }));
    await preview(user);
    await user.click(await screen.findByRole('button', { name: 'Restore stack…' }));
    const confirm = await screen.findByRole('alertdialog');
    expect(within(confirm).queryByText(/overwrites/i)).toBeNull();
    expect(within(confirm).getByText(/volume contents are replaced and not kept/i)).toBeTruthy();
  });

  it('offers a new name with the server message when the compose project is taken', async () => {
    setup({
      'POST /api/backups/restore/plan': {
        status: 409,
        body: { error: 'compose project web belongs to another stack', code: 'project_taken' },
      },
    });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    await preview(user);
    expect(await screen.findByText(/belongs to another stack/)).toBeTruthy();
    expect(screen.getByLabelText('Restore as new name')).toBeTruthy();
  });

  it('explains nothing_to_restore without echoing the raw message', async () => {
    setup({
      'POST /api/backups/restore/plan': { status: 400, body: { error: 'backup: choose', code: 'nothing_to_restore' } },
    });
    const user = await openDialog();
    await screen.findByRole('combobox', { name: 'Stack' });
    await preview(user);
    expect(await screen.findByText(/Choose the files, the volumes or the images/i)).toBeTruthy();
  });
});
