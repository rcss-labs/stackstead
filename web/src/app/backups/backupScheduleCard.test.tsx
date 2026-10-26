import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import { createBackupsApi } from '$lib/api/backups';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type Routes } from '../../vitest-setup';
import { BackupScheduleCard } from './BackupScheduleCard';
import { sampleConfig } from './testKit';

let fake: FakeFetch;
afterEach(() => fake.restore());

function mount(routes: Routes = {}, config = sampleConfig) {
  fake = installFakeFetch({ 'GET /api/backups/config': { body: config }, ...routes });
  return render(<BackupScheduleCard api={createBackupsApi('')} />);
}
const calls = (method: string, path: string) => fake.calls.filter((c) => c.method === method && c.path === path);

describe('BackupScheduleCard', () => {
  it('loads the config and shows that a secret is set', async () => {
    mount();
    expect(await screen.findByText('Secret set')).toBeTruthy();
    expect((screen.getByLabelText('Path') as HTMLInputElement).value).toBe('/var/backups');
  });

  it('saves without secretKey when the field was untouched', async () => {
    mount({ 'PUT /api/backups/config': { body: sampleConfig } });
    await userEvent.click(await screen.findByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls('PUT', '/api/backups/config')).toHaveLength(1));
    const body = calls('PUT', '/api/backups/config')[0].body as { s3: Record<string, unknown> };
    expect('secretKey' in body.s3).toBe(false);
  });

  it('sends the secret once typed', async () => {
    mount({ 'PUT /api/backups/config': { body: sampleConfig } });
    await userEvent.type(await screen.findByLabelText('Secret access key'), 'shh');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls('PUT', '/api/backups/config')).toHaveLength(1));
    expect((calls('PUT', '/api/backups/config')[0].body as { s3: { secretKey: string } }).s3.secretKey).toBe('shh');
  });

  it('shows the weekday select only for weekly', async () => {
    mount();
    await screen.findByText('Secret set');
    expect(screen.queryByLabelText('Weekday')).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: 'Weekly' }));
    expect(screen.getByLabelText('Weekday')).toBeTruthy();
  });

  it('shows the next run as an absolute local time, not a relative one', async () => {
    mount();
    await screen.findByText('Secret set');
    const expected = new Date(sampleConfig.nextRun!).toLocaleString(undefined, {
      weekday: 'short',
      hour: '2-digit',
      minute: '2-digit',
    });
    expect(screen.getByText(`Next run ${expected}`)).toBeTruthy();
    expect(screen.queryByText(/just now/)).toBeNull();
  });

  it('limits Keep last to 1..1000', async () => {
    mount();
    const keep = (await screen.findByLabelText('Keep last')) as HTMLInputElement;
    expect(keep.min).toBe('1');
    expect(keep.max).toBe('1000');
    expect(screen.queryByText(/0 keeps everything/)).toBeNull();
  });

  it('clears the stored secret with a DELETE', async () => {
    mount({ 'DELETE /api/backups/config/s3-secret': { status: 204 } });
    await userEvent.click(await screen.findByRole('button', { name: 'Clear secret' }));
    await waitFor(() => expect(calls('DELETE', '/api/backups/config/s3-secret')).toHaveLength(1));
    expect(await screen.findByText('Secret not set')).toBeTruthy();
  });

  it('posts a run now and toasts the error on 409', async () => {
    mount({ 'POST /api/backups/run': { status: 409, body: { error: 'a backup is already running', code: 'backup_running' } } });
    const seen: string[] = [];
    const off = toasts.subscribe((list) => seen.push(...list.map((t) => t.message)));
    await userEvent.click(await screen.findByRole('button', { name: 'Back up now' }));
    await waitFor(() => expect(calls('POST', '/api/backups/run')).toHaveLength(1));
    await waitFor(() => expect(seen.some((m) => m.includes('already running'))).toBe(true));
    off();
  });
});
