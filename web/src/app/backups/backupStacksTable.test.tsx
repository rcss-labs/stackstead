import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { createBackupsApi } from '$lib/api/backups';
import type { BackupStack } from '$lib/api/backupTypes';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';
import { BackupStacksTable } from './BackupStacksTable';

let fake: FakeFetch;
afterEach(() => fake.restore());

const web: BackupStack = {
  stack: 'web',
  volumes: false,
  images: false,
  stopDuring: true,
  mounts: [
    { kind: 'volume', name: 'data', target: '/data', bytes: 2048 },
    { kind: 'bind', name: '/srv/x', target: '/x', bytes: -1 },
  ],
};

function mount() {
  fake = installFakeFetch({ 'GET /api/backups/stacks': { body: [web] }, 'PUT /api/backups/stacks/web': { status: 204 } });
  return render(<BackupStacksTable api={createBackupsApi('')} />);
}

describe('BackupStacksTable', () => {
  it('disables Stop while Volumes is unchecked', async () => {
    mount();
    const stop = await screen.findByLabelText('Stop while backing up web');
    expect((stop as HTMLInputElement).disabled).toBe(true);
  });

  it('saves immediately when Volumes is toggled', async () => {
    mount();
    await userEvent.click(await screen.findByLabelText('Volumes web'));
    await waitFor(() => expect(fake.calls.filter((c) => c.method === 'PUT')).toHaveLength(1));
    const put = fake.calls.find((c) => c.method === 'PUT')!;
    expect(put.path).toBe('/api/backups/stacks/web');
    expect(put.body).toEqual({ volumes: true, images: false, stopDuring: true });
  });

  it('lists mounts with sizes, using a dash when unknown', async () => {
    mount();
    await userEvent.click(await screen.findByRole('button', { name: 'Show mounts of web' }));
    expect(screen.getByText('2.0 KiB')).toBeTruthy();
    expect(screen.getByText('—')).toBeTruthy();
  });
});
