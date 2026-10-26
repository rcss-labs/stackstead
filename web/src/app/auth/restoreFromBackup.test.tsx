import { cleanup, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { onUnauthorized } from '$lib/api/client';
import { expireSession } from '$lib/api/session';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream, type Routes } from '../../vitest-setup';
import { currentPath, mountAuth, needsSetup } from './authTestKit';

let fake: FakeFetch;
let stream: FakeStream | undefined;
beforeEach(() => {
  onUnauthorized(() => {});
  toasts.reset();
});
afterEach(async () => {
  cleanup();
  expireSession();
  stream?.restore();
  stream = undefined;
  fake?.restore();
  await new Promise((r) => setTimeout(r, 5)); // useStore releases subscriptions on the next task
});

const summary = {
  manifest: {
    format: 1,
    stacksteadVersion: '1.0.0',
    host: 'oldbox',
    hostSlug: 'oldbox',
    instanceId: 'i',
    createdAt: '2026-10-25T03:00:00Z',
    status: 'success',
    stacks: [{ name: 'web', wasRunning: true, volumes: [], images: [] }],
    networks: 0,
    items: [],
  },
  conflicts: [{ kind: 'volume', name: 'web_data', action: 'overwrite' }],
  conflictsDigest: 'dg1',
};

async function mountRestore(routes: Routes = {}) {
  const boot = await needsSetup();
  boot.restore();
  fake = installFakeFetch({
    'GET /api/setup/status': { body: { needsSetup: true } },
    'POST /api/setup/restore/inspect': { body: summary },
    ...routes,
  });
  mountAuth('/setup');
  const user = userEvent.setup();
  await user.click(screen.getByRole('button', { name: 'Restore from backup' }));
  return user;
}
const calls = (method: string, path: string) => fake.calls.filter((c) => c.method === method && c.path === path);

async function inspectPath(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByRole('tab', { name: 'Path' }));
  await user.type(screen.getByLabelText('Archive path'), '/backups/a.tar.gz');
  await user.click(screen.getByRole('button', { name: 'Inspect archive' }));
  await screen.findByText('web_data');
}

describe('Restore from backup', () => {
  it('offers both choices on the setup screen', async () => {
    fake = await needsSetup();
    mountAuth('/setup');
    expect(screen.getByRole('button', { name: 'Create admin' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Restore from backup' })).toBeTruthy();
    expect(screen.getByLabelText('Username')).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'Restore from backup' }));
    expect(screen.queryByLabelText('Username')).toBeNull();
    expect(screen.getByRole('tab', { name: 'S3' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Upload' })).toBeTruthy();
  });

  it('posts an uploaded archive to the upload route, then inspects it', async () => {
    const user = await mountRestore({ 'POST /api/setup/restore/upload': { body: { source: { kind: 'upload' } } } });
    await user.click(screen.getByRole('tab', { name: 'Upload' }));
    await user.type(screen.getByLabelText(/Setup token/), 'tok-9');
    await user.upload(screen.getByLabelText('Archive file'), new File(['x'], 'a.tar.gz', { type: 'application/gzip' }));
    await user.click(screen.getByRole('button', { name: 'Upload and inspect' }));
    await screen.findByText('web_data');
    const upload = calls('POST', '/api/setup/restore/upload')[0];
    expect(upload.headers.get('Content-Type')).toBe('application/gzip');
    expect(upload.headers.get('X-Stackstead-Setup-Token')).toBe('tok-9');
    expect((calls('POST', '/api/setup/restore/inspect')[0].body as { source: unknown }).source).toEqual({ kind: 'upload' });
  });

  it('sends S3 details with the inspect request', async () => {
    const user = await mountRestore();
    await user.click(screen.getByRole('tab', { name: 'S3' }));
    await user.type(screen.getByLabelText('Bucket and key, or s3:// URL'), 's3://my-bucket/host/a.tar.gz');
    await user.type(screen.getByLabelText('Region'), 'eu-west-1');
    await user.type(screen.getByLabelText('Access key ID'), 'AKIA1');
    await user.type(screen.getByLabelText('Secret access key'), 'shh');
    await user.click(screen.getByRole('button', { name: 'Inspect archive' }));
    await screen.findByText('web_data');
    const body = calls('POST', '/api/setup/restore/inspect')[0].body as { source: { kind: string; s3: Record<string, unknown> } };
    expect(body.source.kind).toBe('s3');
    expect(body.source.s3).toMatchObject({ bucket: 'my-bucket', key: 'host/a.tar.gz', region: 'eu-west-1', accessKeyId: 'AKIA1', secretKey: 'shh' });
  });

  it('shows the summary and each conflict with its action', async () => {
    const user = await mountRestore();
    await inspectPath(user);
    expect(screen.getByText(/oldbox/)).toBeTruthy();
    expect(screen.getByText('overwrite')).toBeTruthy();
    expect(screen.getByText('web_data')).toBeTruthy();
  });

  it('does not start until the conflicts are confirmed, then sends the inspected digest', async () => {
    const user = await mountRestore({
      'POST /api/setup/restore/start': { status: 202, body: { restoreToken: 'rt', operation: { id: 'op', state: 'running' } } },
    });
    stream = installFakeStream({ '/api/setup/restore/progress': [] });
    await inspectPath(user);
    const start = screen.getByRole('button', { name: 'Start restore' });
    await user.click(start);
    expect(calls('POST', '/api/setup/restore/start')).toHaveLength(0);

    expect((screen.getByLabelText('Start stacks that were running') as HTMLInputElement).checked).toBe(true);
    await user.click(screen.getByLabelText(/I understand/));
    await user.click(start);
    await waitFor(() => expect(calls('POST', '/api/setup/restore/start')).toHaveLength(1));
    expect(calls('POST', '/api/setup/restore/start')[0].body).toMatchObject({
      source: { kind: 'path', path: '/backups/a.tar.gz' },
      startStacks: true,
      confirmedDigest: 'dg1',
    });
  });

  it('polls the setup status after the stream ends and routes to login', async () => {
    let polls = 0;
    const user = await mountRestore({
      'POST /api/setup/restore/start': { status: 202, body: { restoreToken: 'rt', operation: { id: 'op', state: 'running' } } },
      'GET /api/setup/status': () => ({ body: { needsSetup: ++polls < 2 } }),
    });
    stream = installFakeStream({ '/api/setup/restore/progress': ['event: line\ndata: "restoring volumes"\n\n'] });
    await inspectPath(user);
    await user.click(screen.getByLabelText(/I understand/));
    await user.click(screen.getByRole('button', { name: 'Start restore' }));
    await screen.findByText('restoring volumes');
    expect(stream.requests[0].path).toContain('token=rt');
    stream.close('/api/setup/restore/progress'); // the server re-execs: no `done` event
    await waitFor(() => expect(currentPath()).toBe('/login'), { timeout: 6000 });
    expect(polls).toBeGreaterThanOrEqual(2);
  });
});
