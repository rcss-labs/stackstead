import { render, screen, waitFor } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { get } from 'svelte/store';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { navigate, route } from '../lib/router';
import { toasts } from '../lib/stores/toasts';
import { installFakeFetch, type FakeFetch } from '../vitest-setup';
import StackCreate from './StackCreate.svelte';

let fake: FakeFetch;
const SUMMARY = { name: 'web', path: '/srv/stacks/web/compose.yaml', project: 'web', origin: 'managed', status: 'stopped', serviceCount: 1, running: 0 };
const OP = { id: 'op1', stack: 'web', action: 'up', state: 'running', startedAt: '2026-10-14T10:00:00Z' };

function routes(extra: Record<string, unknown> = {}) {
  fake = installFakeFetch({
    'GET /api/networks': { body: [] },
    'GET /api/stacks': { body: [] },
    'GET /api/operations?state=running': { body: [] },
    'POST /api/stacks': { status: 201, body: SUMMARY },
    'POST /api/stacks/web/actions/up': { status: 202, body: OP },
    'GET /api/fs': { body: [{ name: '/srv/app', isDir: true, size: 0 }] },
    'GET /api/fs?path=%2Fsrv%2Fapp': { body: [{ name: 'Dockerfile', isDir: false, size: 5 }, { name: 'build.Dockerfile', isDir: false, size: 5 }] },
    ...(extra as object),
  } as never);
}

beforeEach(() => navigate('/stacks/new'));
afterEach(() => {
  fake?.restore();
  toasts.reset();
});

async function fillImage(user: ReturnType<typeof userEvent.setup>, name = 'web') {
  await user.type(screen.getByLabelText('Stack name'), name);
  await user.type(screen.getByLabelText('Image'), 'nginx:1.27');
}
const submit = (user: ReturnType<typeof userEvent.setup>) => user.click(screen.getByRole('button', { name: 'Create stack' }));
const post = () => fake.calls.find((c) => c.method === 'POST' && c.path === '/api/stacks');

describe('StackCreate', () => {
  it('Cancel is a router link', () => {
    routes();
    render(StackCreate);
    const a = screen.getByRole('link', { name: 'Cancel' });
    a.addEventListener('click', (e) => e.preventDefault());
    navigate('/stacks/new');
    a.click();
    expect(get(route).path).toBe('/');
  });

  it('posts the request, starts up and navigates to the operation', async () => {
    routes();
    const user = userEvent.setup();
    render(StackCreate);
    await fillImage(user);
    expect((screen.getByLabelText('Service name') as HTMLInputElement).value).toBe('web');
    await submit(user);
    await waitFor(() => expect(get(route).path).toBe('/stacks/web'));
    expect(post()?.body).toEqual({ name: 'web', service: 'web', config: { image: 'nginx:1.27', limits: {} } });
    expect(fake.calls.some((c) => c.path === '/api/stacks/web/actions/up')).toBe(true);
    expect(get(route).query.get('tab')).toBe('operations');
    expect(get(route).query.get('op')).toBe('op1');
  });

  it('with "Start now" unchecked no action is started', async () => {
    routes();
    const user = userEvent.setup();
    render(StackCreate);
    await fillImage(user);
    await user.click(screen.getByLabelText(/Start now/));
    await submit(user);
    await waitFor(() => expect(get(route).path).toBe('/stacks/web'));
    expect(fake.calls.some((c) => c.path.includes('/actions/'))).toBe(false);
    expect(get(route).query.get('op')).toBeNull();
  });

  it('an edited service name is kept and sent', async () => {
    routes();
    const user = userEvent.setup();
    render(StackCreate);
    await fillImage(user);
    const svc = screen.getByLabelText('Service name');
    await user.clear(svc);
    await user.type(svc, 'app');
    await submit(user);
    await waitFor(() => expect(post()).toBeTruthy());
    expect(post()?.body).toMatchObject({ name: 'web', service: 'app' });
  });

  it('client validation blocks the request and focuses the first error', async () => {
    routes();
    const user = userEvent.setup();
    render(StackCreate);
    await user.type(screen.getByLabelText('Stack name'), 'Bad Name');
    await submit(user);
    const field = screen.getByLabelText('Stack name');
    await waitFor(() => expect(document.activeElement).toBe(field));
    expect(field.getAttribute('aria-invalid')).toBe('true');
    expect(post()).toBeUndefined();
  });

  it('409 puts the message under the stack name, not the service name', async () => {
    routes({ 'POST /api/stacks': { status: 409, body: { error: 'stack already exists' } } });
    const user = userEvent.setup();
    render(StackCreate);
    await fillImage(user);
    await submit(user);
    expect(await screen.findByText('stack already exists')).toBeTruthy();
    const stack = screen.getByLabelText('Stack name');
    await waitFor(() => expect(document.activeElement).toBe(stack));
    expect(screen.getByLabelText('Service name').getAttribute('aria-invalid')).toBeNull();
    expect(get(route).path).toBe('/stacks/new');
  });

  it('422 shows the compose error verbatim under the form', async () => {
    routes({ 'POST /api/stacks': { status: 422, body: { error: 'services.web.image: invalid\nline two' } } });
    const user = userEvent.setup();
    render(StackCreate);
    await fillImage(user);
    await submit(user);
    const alert = await screen.findByText(/services\.web\.image: invalid/);
    expect(alert.textContent).toBe('services.web.image: invalid\nline two');
  });

  it('400 on build.context lands on the Build context field', async () => {
    routes({ 'POST /api/stacks': { status: 400, body: { error: 'invalid', fields: [{ field: 'build.context', message: 'path is outside the allowed roots' }] } } });
    const user = userEvent.setup();
    render(StackCreate);
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await user.click(screen.getByLabelText('From Dockerfile'));
    await user.type(screen.getByLabelText('Build context'), '/etc');
    await submit(user);
    expect(await screen.findByText('path is outside the allowed roots')).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Build context')));
  });

  it('403 (filesystem permission) is shown under the form', async () => {
    routes({ 'POST /api/stacks': { status: 403, body: { error: 'permission denied' } } });
    const user = userEvent.setup();
    render(StackCreate);
    await fillImage(user);
    await submit(user);
    const alert = await screen.findByText('permission denied');
    expect(alert.getAttribute('role')).toBe('alert');
  });

  it('a stale build error is dropped when switching back to image', async () => {
    routes({ 'POST /api/stacks': { status: 400, body: { error: 'invalid', fields: [{ field: 'build.context', message: 'bad context' }] } } });
    const user = userEvent.setup();
    render(StackCreate);
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await user.click(screen.getByLabelText('From Dockerfile'));
    await user.type(screen.getByLabelText('Build context'), '/etc');
    await submit(user);
    await screen.findByText('bad context');
    await user.click(screen.getByLabelText('From Image'));
    await user.click(screen.getByLabelText('From Dockerfile'));
    expect(screen.queryByText('bad context')).toBeNull();
  });

  it('a Dockerfile picked in a root context is stored relative', async () => {
    routes({
      'GET /api/fs': { body: [{ name: '/', isDir: true, size: 0 }] },
      'GET /api/fs?path=%2F': { body: [{ name: 'Dockerfile', isDir: false, size: 5 }] },
    });
    const user = userEvent.setup();
    render(StackCreate);
    await user.click(screen.getByLabelText('From Dockerfile'));
    await user.type(screen.getByLabelText('Build context'), '/');
    await user.click(screen.getByRole('button', { name: 'Browse for Dockerfile' }));
    await user.click(await screen.findByRole('button', { name: '/' }));
    await user.click(await screen.findByRole('button', { name: /^Dockerfile/ }));
    await waitFor(() => expect((screen.getByLabelText('Dockerfile') as HTMLInputElement).value).toBe('Dockerfile'));
  });

  it('400 field errors land on their fields', async () => {
    routes({ 'POST /api/stacks': { status: 400, body: { error: 'invalid', fields: [{ field: 'ports[0]', message: 'port in use' }] } } });
    const user = userEvent.setup();
    render(StackCreate);
    await fillImage(user);
    await user.click(screen.getByRole('button', { name: 'Add port' }));
    await user.type(screen.getByLabelText('Port 1'), '8080:80');
    await submit(user);
    expect(await screen.findByText('port in use')).toBeTruthy();
  });

  it('503 shows the unavailable banner and stays on the page', async () => {
    routes({ 'POST /api/stacks': { status: 503, body: { error: 'docker unavailable' } } });
    const user = userEvent.setup();
    render(StackCreate);
    await fillImage(user);
    await submit(user);
    const banner = await screen.findByText(/the stack was not created/);
    await waitFor(() => expect(document.activeElement?.contains(banner)).toBe(true));
    expect(get(route).path).toBe('/stacks/new');
  });

  it('a partial-checks response raises a warning toast', async () => {
    routes({ 'POST /api/stacks': { status: 201, body: SUMMARY, headers: { 'X-Stackstead-Checks': 'partial' } } });
    const user = userEvent.setup();
    render(StackCreate);
    await fillImage(user);
    await submit(user);
    await waitFor(() => expect(get(route).path).toBe('/stacks/web'));
    expect(get(toasts).some((t) => t.kind === 'warning')).toBe(true);
  });

  it('a picked Dockerfile inside the context is stored relative to it', async () => {
    routes();
    const user = userEvent.setup();
    render(StackCreate);
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await user.click(screen.getByLabelText('From Dockerfile'));
    await user.type(screen.getByLabelText('Build context'), '/srv/app');
    await user.click(screen.getByRole('button', { name: 'Browse for Dockerfile' }));
    await user.click(await screen.findByRole('button', { name: '/srv/app' }));
    await user.click(await screen.findByRole('button', { name: /^build\.Dockerfile/ }));
    await waitFor(() => expect((screen.getByLabelText('Dockerfile') as HTMLInputElement).value).toBe('build.Dockerfile'));
  });
});
