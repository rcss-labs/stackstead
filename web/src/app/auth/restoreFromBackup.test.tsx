import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes as RouterRoutes } from 'react-router';
import { RestoreFromBackup } from './RestoreFromBackup';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { onUnauthorized } from '$lib/api/client';
import { expireSession } from '$lib/api/session';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream, type Routes, type StreamRoutes } from '../../vitest-setup';
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
    status: 'partial',
    stacks: [{ name: 'web', wasRunning: true, volumes: [{ key: 'a' }, { key: 'b' }], images: ['nginx:1'] }],
    networks: 0,
    items: [],
  },
  conflicts: [{ kind: 'volume', name: 'web_data', action: 'overwrite' }],
  conflictsDigest: 'dg1',
  encrypted: true,
  keyId: '0123456789abcdef',
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
    stream = installFakeStream({
      '/api/setup/restore/progress': { responses: [{ chunks: ['event: line\ndata: "restoring volumes"\n\n'] }, { status: 403, body: { error: 'x' } }] },
    }); // after the re-exec the new process answers 403
    await inspectPath(user);
    await user.click(screen.getByLabelText(/I understand/));
    await user.click(screen.getByRole('button', { name: 'Start restore' }));
    await screen.findByText('restoring volumes');
    expect(stream.requests[0].path).not.toContain('rt'); // the token stays out of the URL (proxy logs)
    expect(stream.requests[0].headers.get('X-Restore-Token')).toBe('rt');
    stream?.close('/api/setup/restore/progress'); // the server re-execs: no `done` event
    await waitFor(() => expect(currentPath()).toBe('/login'), { timeout: 6000 });
    expect(polls).toBeGreaterThanOrEqual(2);
  });

  it('explains a 409 upload_running and a 409 restore_running', async () => {
    const user = await mountRestore({
      'POST /api/setup/restore/upload': { status: 409, body: { error: 'x', code: 'upload_running' } },
    });
    await user.click(screen.getByRole('tab', { name: 'Upload' }));
    await user.upload(screen.getByLabelText('Archive file'), new File(['x'], 'a.tar.gz'));
    await user.click(screen.getByRole('button', { name: 'Upload and inspect' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/Another upload is already in progress/);
  });

  it('explains restore_running when starting', async () => {
    const user = await mountRestore({
      'POST /api/setup/restore/start': { status: 409, body: { error: 'x', code: 'restore_running' } },
    });
    await inspectPath(user);
    await user.click(screen.getByLabelText(/I understand/));
    await user.click(screen.getByRole('button', { name: 'Start restore' }));
    expect((await screen.findByRole('alert')).textContent).toMatch(/already running or waiting/);
  });

  it('shows volume and image counts, the archive status and the identity warning', async () => {
    const user = await mountRestore();
    await inspectPath(user);
    expect(screen.getByText('Volumes').nextSibling?.textContent).toBe('2');
    expect(screen.getByText('Images').nextSibling?.textContent).toBe('1');
    expect(screen.getByText('Status').nextSibling?.textContent).toBe('partial');
    expect(screen.getByText("This host will take over the backed-up host's identity; don't run both at once.")).toBeTruthy();
  });

  it('is not held to the narrow login card in restore mode', async () => {
    const user = await mountRestore();
    const card = screen.getByRole('button', { name: 'Create admin' }).closest('form') as HTMLElement;
    expect(card.style.maxWidth).toBe('');
    await user.click(screen.getByRole('button', { name: 'Create admin' }));
    expect(card.style.maxWidth).toBe('400px');
  });
});

describe('Restore progress after the stream ends', () => {
  async function mountDirect(routes: Routes, deadlineMs: number, streamRoutes: StreamRoutes) {
    fake = installFakeFetch({ 'POST /api/setup/restore/inspect': { body: summary }, ...routes });
    stream = installFakeStream(streamRoutes);
    render(
      <MemoryRouter initialEntries={['/setup']}>
        <RouterRoutes>
          <Route path="/setup" element={<RestoreFromBackup pollMs={10} deadlineMs={deadlineMs} />} />
          <Route path="*" element={<p>elsewhere</p>} />
        </RouterRoutes>
      </MemoryRouter>,
    );
    const user = userEvent.setup();
    await inspectPath(user);
    await user.click(screen.getByLabelText(/I understand/));
    await user.click(screen.getByRole('button', { name: 'Start restore' }));
    return user;
  }
  const started = { 'POST /api/setup/restore/start': { status: 202, body: { restoreToken: 'rt', operation: { id: 'op', state: 'running' } } } };

  it('says so when the server does not come back', async () => {
    await mountDirect({ ...started, 'GET /api/setup/status': { body: { needsSetup: true } } }, 150, {
      '/api/setup/restore/progress': { responses: [{ chunks: ['event: line\ndata: "x"\n\n'] }, { status: 403, body: { error: 'x' } }] },
    });
    await screen.findByText('x');
    stream?.close('/api/setup/restore/progress');
    expect((await screen.findByRole('alert', {}, { timeout: 3000 })).textContent).toContain(
      'Stackstead did not come back — check the service log or supervisor',
    );
  });

  it('keeps following the stream across a reconnect and shows a failed restore', async () => {
    await mountDirect({ ...started, 'GET /api/setup/status': { body: { needsSetup: true } } }, 60_000, {
      '/api/setup/restore/progress': {
        responses: [
          { chunks: ['event: line\ndata: "restoring"\n\n'] },
          { chunks: ['event: done\ndata: {"state":"failed","error":"volume fill failed"}\n\n'] },
        ],
      },
    });
    await screen.findByText('restoring');
    stream?.close('/api/setup/restore/progress'); // a dropped connection, not a re-exec
    expect((await screen.findByRole('alert', {}, { timeout: 5000 })).textContent).toContain('volume fill failed');
  });

  it('does not declare a restore failed when the stream recovers before the deadline', async () => {
    await mountDirect({ ...started, 'GET /api/setup/status': { body: { needsSetup: true } } }, 2500, {
      '/api/setup/restore/progress': {
        responses: [
          { chunks: ['id: 0\nevent: line\ndata: "first"\n\n'] },
          { chunks: ['id: 1\nevent: line\ndata: "second"\n\n'] },
        ],
      },
    });
    await screen.findByText('first');
    stream?.close('/api/setup/restore/progress'); // one dropped connection
    await screen.findByText('second', {}, { timeout: 4000 });
    await new Promise((r) => setTimeout(r, 3000)); // well past deadlineMs, stream still open
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.queryByText(/Waiting for Stackstead to restart/)).toBeNull();
  }, 15_000);

  it('does not repeat lines the server replays after a reconnect', async () => {
    await mountDirect({ ...started, 'GET /api/setup/status': { body: { needsSetup: true } } }, 60_000, {
      '/api/setup/restore/progress': {
        responses: [
          { chunks: ['id: 0\nevent: line\ndata: "alpha"\n\n'] },
          { chunks: ['id: 0\nevent: line\ndata: "alpha"\n\n', 'id: 1\nevent: line\ndata: "beta"\n\n'] },
        ],
      },
    });
    await screen.findByText('alpha');
    stream?.close('/api/setup/restore/progress');
    await screen.findByText('beta', {}, { timeout: 4000 });
    expect(screen.getAllByText('alpha')).toHaveLength(1);
  }, 10_000);
});

describe('Restore of encrypted and unencrypted archives', () => {
  const startRoute = { status: 202, body: { restoreToken: 'rt', operation: { id: 'op', state: 'running' } } };

  it('sends the passphrase with inspect and start', async () => {
    const user = await mountRestore({ 'POST /api/setup/restore/start': startRoute });
    stream = installFakeStream({ '/api/setup/restore/progress': [] });
    await user.click(screen.getByRole('tab', { name: 'Path' }));
    await user.type(screen.getByLabelText('Archive path'), '/backups/a.tar.gz.enc');
    await user.type(screen.getByLabelText('Archive passphrase'), 'open sesame passphrase');
    await user.click(screen.getByRole('button', { name: 'Inspect archive' }));
    await screen.findByText('web_data');
    expect(calls('POST', '/api/setup/restore/inspect')[0].body).toMatchObject({ passphrase: 'open sesame passphrase' });
    expect(screen.getByText('Encrypted, key 0123456789abcdef')).toBeTruthy();
    expect(screen.queryByText(/not encrypted or signed/)).toBeNull();

    await user.click(screen.getByLabelText(/I understand/));
    await user.click(screen.getByRole('button', { name: 'Start restore' }));
    await waitFor(() => expect(calls('POST', '/api/setup/restore/start')).toHaveLength(1));
    expect(calls('POST', '/api/setup/restore/start')[0].body).toMatchObject({
      passphrase: 'open sesame passphrase',
      allowUnencrypted: false,
    });
  });

  it('omits the passphrase when none was typed', async () => {
    const user = await mountRestore();
    await inspectPath(user);
    expect('passphrase' in (calls('POST', '/api/setup/restore/inspect')[0].body as object)).toBe(false);
  });

  it('requires acknowledging an unencrypted archive before starting', async () => {
    const user = await mountRestore({
      'POST /api/setup/restore/inspect': { body: { ...summary, encrypted: false, keyId: '' } },
      'POST /api/setup/restore/start': startRoute,
    });
    stream = installFakeStream({ '/api/setup/restore/progress': [] });
    await inspectPath(user);
    expect(
      screen.getByText(/This archive is not encrypted or signed\. Anyone who could write to its storage could have changed it/),
    ).toBeTruthy();
    await user.click(screen.getByLabelText(/I understand/));
    await user.click(screen.getByRole('button', { name: 'Start restore' }));
    expect(calls('POST', '/api/setup/restore/start')).toHaveLength(0);

    await user.click(screen.getByLabelText('I trust the source of this archive'));
    await user.click(screen.getByRole('button', { name: 'Start restore' }));
    await waitFor(() => expect(calls('POST', '/api/setup/restore/start')).toHaveLength(1));
    expect(calls('POST', '/api/setup/restore/start')[0].body).toMatchObject({ allowUnencrypted: true });
  });

  it.each([
    ['passphrase_required', 'This archive is encrypted. Enter its passphrase.'],
    ['wrong_passphrase', 'That passphrase does not open this archive.'],
    ['archive_tampered', 'The archive failed its integrity check. Do not restore it.'],
    ['unencrypted_archive', 'This archive is not encrypted'],
  ])('explains %s', async (code, text) => {
    const user = await mountRestore({
      'POST /api/setup/restore/inspect': { status: 422, body: { error: 'raw server text', code } },
    });
    await user.click(screen.getByRole('tab', { name: 'Path' }));
    await user.type(screen.getByLabelText('Archive path'), '/backups/a.tar.gz.enc');
    await user.click(screen.getByRole('button', { name: 'Inspect archive' }));
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toContain(text);
    expect(alert.textContent).not.toContain('raw server text');
  });
});
