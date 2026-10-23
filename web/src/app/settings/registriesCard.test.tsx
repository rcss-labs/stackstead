import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it } from 'vitest';
import type { RegistryView } from '$lib/api/imageTypes';
import { installFakeFetch, type FakeFetch, type Routes } from '../../vitest-setup';
import { RegistriesCard } from './RegistriesCard';

let fake: FakeFetch;
afterEach(() => fake.restore());

const HUB: RegistryView = {
  id: 'r1', name: 'Docker Hub', kind: 'dockerhub', host: 'docker.io', username: 'pavel', region: '', mode: '', profile: '',
  accountId: '', configured: true, lastTestAt: '2026-10-20T10:00:00Z', lastTestOk: true, lastTestMessage: 'ok',
};
const ECR: RegistryView = { ...HUB, id: 'r2', name: 'Prod ECR', kind: 'ecr', host: '1.dkr.ecr.eu-west-1.amazonaws.com', username: '', region: 'eu-west-1', mode: 'role' };

function mount(routes: Routes = {}, list: RegistryView[] = [HUB, ECR]) {
  fake = installFakeFetch({ 'GET /api/settings/registries': { body: list }, ...routes });
  return render(<RegistriesCard />);
}
const calls = (method: string, path: string) => fake.calls.filter((c) => c.method === method && c.path === path);

describe('RegistriesCard', () => {
  it('lists registries with kind and host', async () => {
    mount();
    expect((await screen.findAllByText('Docker Hub')).length).toBeGreaterThan(0);
    expect(screen.getByText('Prod ECR')).toBeTruthy();
    expect(screen.getByText('docker.io')).toBeTruthy();
  });

  it('adds a Docker Hub registry', async () => {
    mount({ 'POST /api/settings/registries': { status: 201, body: HUB } }, []);
    await userEvent.click(await screen.findByRole('button', { name: 'Add registry' }));
    const dialog = await screen.findByRole('dialog');
    await userEvent.type(within(dialog).getByLabelText('Name'), 'Hub');
    await userEvent.type(within(dialog).getByLabelText('Username'), 'pavel');
    const token = within(dialog).getByLabelText('Token') as HTMLInputElement;
    expect(token.type).toBe('password');
    await userEvent.type(token, 's3cret');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls('POST', '/api/settings/registries')).toHaveLength(1));
    expect(calls('POST', '/api/settings/registries')[0].body).toEqual({ name: 'Hub', kind: 'dockerhub', hub: { username: 'pavel', token: 's3cret' } });
  });

  it('edits with an empty token field that says to leave it blank, and sends a blank token', async () => {
    mount({ 'PUT /api/settings/registries/r1': { body: HUB } });
    await userEvent.click(await screen.findByRole('button', { name: 'Edit Docker Hub' }));
    const dialog = await screen.findByRole('dialog');
    const token = within(dialog).getByLabelText('Token') as HTMLInputElement;
    expect(token.value).toBe('');
    expect(within(dialog).getByText(/leave blank to keep/i)).toBeTruthy();
    expect((within(dialog).getByLabelText('Username') as HTMLInputElement).value).toBe('pavel');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls('PUT', '/api/settings/registries/r1')).toHaveLength(1));
    expect(calls('PUT', '/api/settings/registries/r1')[0].body).toEqual({ name: 'Docker Hub', kind: 'dockerhub', hub: { username: 'pavel', token: '' } });
  });

  it('shows only the chosen ECR mode fields', async () => {
    mount({}, []);
    await userEvent.click(await screen.findByRole('button', { name: 'Add registry' }));
    const dialog = await screen.findByRole('dialog');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Amazon ECR' }));
    expect(within(dialog).getByLabelText('Access key ID')).toBeTruthy();
    expect(within(dialog).getByLabelText('Secret access key')).toBeTruthy();
    expect(within(dialog).queryByLabelText('Profile')).toBeNull();
    await userEvent.click(within(dialog).getByRole('button', { name: 'AWS profile' }));
    expect(within(dialog).getByLabelText('Profile')).toBeTruthy();
    expect(within(dialog).queryByLabelText('Access key ID')).toBeNull();
    await userEvent.click(within(dialog).getByRole('button', { name: 'IAM role' }));
    expect(within(dialog).queryByLabelText('Profile')).toBeNull();
    expect(within(dialog).queryByLabelText('Access key ID')).toBeNull();
    expect(within(dialog).getByLabelText('Region')).toBeTruthy();
  });

  it('edits a static ECR registry: access key ID prefilled, secrets blank', async () => {
    const STATIC: RegistryView = { ...ECR, mode: 'static', accessKeyId: 'AKIA123' };
    mount({ 'PUT /api/settings/registries/r2': { body: STATIC } }, [STATIC]);
    await userEvent.click(await screen.findByRole('button', { name: 'Edit Prod ECR' }));
    const dialog = await screen.findByRole('dialog');
    expect((within(dialog).getByLabelText('Access key ID') as HTMLInputElement).value).toBe('AKIA123');
    expect((within(dialog).getByLabelText('Secret access key') as HTMLInputElement).value).toBe('');
    const name = within(dialog).getByLabelText('Name');
    await userEvent.clear(name);
    await userEvent.type(name, 'Renamed');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls('PUT', '/api/settings/registries/r2')).toHaveLength(1));
    expect(calls('PUT', '/api/settings/registries/r2')[0].body).toEqual({
      name: 'Renamed', kind: 'ecr',
      ecr: { region: 'eu-west-1', mode: 'static', accessKeyId: 'AKIA123', secretAccessKey: '', sessionToken: '', profile: '' },
    });
  });

  it('clears static fields from the payload after switching to the profile mode', async () => {
    mount({ 'POST /api/settings/registries': { status: 201, body: ECR } }, []);
    await userEvent.click(await screen.findByRole('button', { name: 'Add registry' }));
    const dialog = await screen.findByRole('dialog');
    await userEvent.type(within(dialog).getByLabelText('Name'), 'E');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Amazon ECR' }));
    await userEvent.type(within(dialog).getByLabelText('Region'), 'us-east-1');
    await userEvent.type(within(dialog).getByLabelText('Access key ID'), 'AKIA');
    await userEvent.type(within(dialog).getByLabelText('Secret access key'), 'shh');
    await userEvent.click(within(dialog).getByRole('button', { name: 'AWS profile' }));
    await userEvent.type(within(dialog).getByLabelText('Profile'), 'prod');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls('POST', '/api/settings/registries')).toHaveLength(1));
    expect(calls('POST', '/api/settings/registries')[0].body).toEqual({
      name: 'E', kind: 'ecr',
      ecr: { region: 'us-east-1', mode: 'profile', accessKeyId: '', secretAccessKey: '', sessionToken: '', profile: 'prod' },
    });
  });

  it('drops a stale test result after saving', async () => {
    mount({
      'POST /api/settings/registries/r1/test': { body: { ok: true, host: 'docker.io', message: 'login succeeded' } },
      'PUT /api/settings/registries/r1': { body: HUB },
    });
    await userEvent.click(await screen.findByRole('button', { name: 'Test Docker Hub' }));
    expect(await screen.findByText(/login succeeded/)).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'Edit Docker Hub' }));
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.queryByText(/login succeeded/)).toBeNull());
  });

  it('shows the test message inline', async () => {
    mount({ 'POST /api/settings/registries/r1/test': { body: { ok: false, host: 'docker.io', message: 'unauthorized: bad token' } } });
    await userEvent.click(await screen.findByRole('button', { name: 'Test Docker Hub' }));
    expect(await screen.findByText(/unauthorized: bad token/)).toBeTruthy();
  });

  it('confirms before deleting', async () => {
    mount({ 'DELETE /api/settings/registries/r1': { status: 204 } });
    await userEvent.click(await screen.findByRole('button', { name: 'Delete Docker Hub' }));
    expect(calls('DELETE', '/api/settings/registries/r1')).toHaveLength(0);
    await userEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete registry' }));
    await waitFor(() => expect(calls('DELETE', '/api/settings/registries/r1')).toHaveLength(1));
  });
});
