import { cleanup, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { operationsRefresh } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import { findRoute } from '../routes';
import { addImage, fake, fillImage, location, mountAt, post, routes, submit, SUMMARY } from './createTestKit';

vi.mock('$lib/stores/live', async (orig) => {
  const { writable } = await import('$lib/store');
  const store = writable({ connection: 'open', events: 'connected', metricsDegraded: false, docker: 'ok', compose: '2.29.0', dockerVersion: '' });
  const changes = writable<{ project: string; seq: number; resync?: true } | null>(null);
  return {
    ...(await orig<typeof import('$lib/stores/live')>()),
    live: { subscribe: store.subscribe },
    changes: { subscribe: changes.subscribe },
    refreshStatus: vi.fn(),
  };
});

/** The service cards, without the compose.yaml preview region. */
const serviceCards = () => screen.getAllByRole('region').filter((r) => r.getAttribute('aria-labelledby') !== 'preview-heading');

async function mount(extra: Record<string, unknown> = {}) {
  routes(extra);
  mountAt('/stacks/new');
  await screen.findByLabelText('Stack name'); // the screen is a lazy chunk
  return userEvent.setup();
}

// Load the lazy route chunk up front: on a cold cache, transforming it can outlast the first
// test's default findBy timeout.
beforeAll(async () => {
  await import('../routes/StackCreate');
}, 30_000);

afterEach(() => {
  cleanup();
  fake?.restore();
  toasts.reset();
});

describe('New stack screen', () => {
  it('adds a card, opens and focuses it; removes it and reopens the previous one', async () => {
    const user = await mount();
    await fillImage(user);
    expect(screen.queryByRole('button', { name: /^Remove service/ })).toBeNull();
    await user.click(screen.getByRole('button', { name: '+ Add another service' }));
    expect(document.activeElement).toBe(screen.getByLabelText('Service name'));
    expect(screen.getAllByLabelText('Image')).toHaveLength(1);
    const headers = screen.getAllByRole('button').filter((b) => b.hasAttribute('aria-expanded') && b.id.startsWith('service-heading'));
    expect(headers.map((h) => h.getAttribute('aria-expanded'))).toEqual(['false', 'true']);
    await user.type(screen.getByLabelText('Service name'), 'db');
    await user.click(screen.getByRole('button', { name: 'Remove service db' }));
    expect((screen.getByLabelText('Image') as HTMLInputElement).value).toBe('nginx:1.27');
    expect(screen.queryByRole('button', { name: /^Remove service/ })).toBeNull();
  });

  it('card 0 follows the stack name until edited; other cards do not', async () => {
    const user = await mount();
    await fillImage(user);
    await addImage(user);
    await user.type(screen.getByLabelText('Stack name'), 'app');
    expect((screen.getByLabelText('Service name') as HTMLInputElement).value).toBe('db');
    await user.click(screen.getByRole('button', { name: /^webapp nginx/ }));
    expect((screen.getByLabelText('Service name') as HTMLInputElement).value).toBe('webapp');
    await user.clear(screen.getByLabelText('Service name'));
    await user.type(screen.getByLabelText('Service name'), 'api');
    await user.type(screen.getByLabelText('Stack name'), '2');
    expect((screen.getByLabelText('Service name') as HTMLInputElement).value).toBe('api');
  });

  it('sends every card in order and preserves collapsed form values', async () => {
    const user = await mount();
    await fillImage(user);
    await addImage(user);
    await submit(user);
    await waitFor(() => expect(post()).toBeTruthy());
    expect(post()?.body).toEqual({
      name: 'web',
      services: [
        { service: 'web', config: { image: 'nginx:1.27', limits: {} } },
        { service: 'db', config: { image: 'postgres:17', limits: {} } },
      ],
    });
  });

  it('blocks duplicate service names before the request on the later card', async () => {
    const user = await mount();
    await fillImage(user);
    await addImage(user, 'web');
    await submit(user);
    expect(screen.getByText('duplicate service name web')).toBeTruthy();
    expect(document.activeElement).toBe(screen.getByLabelText('Service name'));
    expect(post()).toBeUndefined();
  });

  it.each([true, false])('routes indexed errors and focuses in DOM order (name error: %s)', async (nameError) => {
    const user = await mount({
      'POST /api/stacks': {
        status: 400,
        body: {
          error: 'invalid',
          fields: [
            { field: 'services[1].ports[0]', message: 'port in use' },
            ...(nameError ? [{ field: 'services[1].service', message: 'duplicate service name db' }] : []),
          ],
        },
      },
    });
    await fillImage(user);
    await addImage(user);
    await user.click(screen.getByRole('button', { name: 'Add port' }));
    await user.type(screen.getByLabelText('Port 1'), '8080:80');
    await user.click(screen.getByRole('button', { name: /^web nginx/ }));
    await submit(user);
    await screen.findByText('port in use');
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText(nameError ? 'Service name' : 'Port 1')));
    expect(screen.getByRole('button', { name: /^db postgres/ }).getAttribute('aria-expanded')).toBe('true');
    await user.click(screen.getByRole('button', { name: /^web nginx/ }));
    expect(screen.queryByText('port in use')).toBeNull();
    expect(screen.getByRole('button', { name: /^db postgres/ }).textContent).toContain(nameError ? '2 errors' : '1 error');
  });

  it('marks collapsed cards that have client errors and opens the first invalid card', async () => {
    const user = await mount();
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await user.click(screen.getByRole('button', { name: '+ Add another service' }));
    await user.type(screen.getByLabelText('Service name'), 'db');
    await submit(user);
    expect(screen.getByRole('button', { name: /^db/ }).textContent).toContain('1 error');
    expect(document.activeElement).toBe(screen.getByLabelText('Image'));
  });

  it.each([undefined, []])('announces and focuses plain 400 above the cards (fields: %s)', async (fields) => {
    const user = await mount({ 'POST /api/stacks': { status: 400, body: { error: 'use only one request form', fields } } });
    await fillImage(user);
    await submit(user);
    const alert = await screen.findByRole('alert');
    expect(alert.textContent).toBe('use only one request form');
    await waitFor(() => expect(document.activeElement).toBe(alert));
    expect(alert.compareDocumentPosition(screen.getByRole('region', { name: /^web/ })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('keeps unknown and out-of-range server errors in the page alert', async () => {
    const user = await mount({
      'POST /api/stacks': {
        status: 400,
        body: {
          error: 'invalid',
          fields: [
            { field: 'services[0].unknown', message: 'unknown card field' },
            { field: 'services[9].image', message: 'missing card' },
            { field: 'services', message: 'bad services' },
          ],
        },
      },
    });
    await fillImage(user);
    await submit(user);
    const alert = await screen.findByRole('alert');
    for (const message of ['unknown card field', 'missing card', 'bad services']) expect(alert.textContent).toContain(message);
    await waitFor(() => expect(document.activeElement).toBe(alert));
  });

  it('soft-disables adding services at 20 with a reason', async () => {
    const user = await mount();
    const add = screen.getByRole('button', { name: '+ Add another service' });
    for (let i = 1; i < 20; i++) await user.click(add);
    expect(serviceCards()).toHaveLength(20);
    expect(add.getAttribute('aria-disabled')).toBe('true');
    expect(add.getAttribute('title')).toMatch(/20/);
    await user.click(add);
    expect(serviceCards()).toHaveLength(20);
  });

  it('freezes structure during submission and routes delayed errors to submitted identities', async () => {
    let respond!: (response: { status: number; body: unknown }) => void;
    const user = await mount({ 'POST /api/stacks': () => new Promise((resolve) => { respond = resolve; }) });
    await fillImage(user);
    await addImage(user);
    await submit(user);
    await user.click(screen.getByRole('button', { name: '+ Add another service' }));
    await user.click(screen.getByRole('button', { name: 'Remove service web' }));
    expect(serviceCards()).toHaveLength(2);
    respond({ status: 400, body: { error: 'invalid', fields: [{ field: 'services[1].service', message: 'bad submitted name' }] } });
    await screen.findByText('bad submitted name');
    expect((screen.getByLabelText('Service name') as HTMLInputElement).value).toBe('db');
  });

  it('keeps browser callbacks on their initiating card and prevents switching or removal during browsing', async () => {
    const user = await mount();
    await fillImage(user);
    await addImage(user);
    await user.click(screen.getByLabelText('From Dockerfile'));
    await user.type(screen.getByLabelText('Build context'), '/srv/app');
    await user.click(screen.getByRole('button', { name: 'Browse for Dockerfile' }));
    const dialog = await screen.findByRole('dialog');
    expect(screen.getByRole('button', { name: /^web nginx/ }).hasAttribute('disabled')).toBe(true);
    expect(screen.getByRole('button', { name: 'Remove service db' }).hasAttribute('disabled')).toBe(true);
    await user.click(await within(dialog).findByRole('button', { name: '/srv/app' }));
    await user.click(await within(dialog).findByRole('button', { name: /^build\.Dockerfile/ }));
    expect((screen.getByLabelText('Dockerfile') as HTMLInputElement).value).toBe('build.Dockerfile');
    await user.click(screen.getByRole('button', { name: /^web nginx/ }));
    expect((screen.getByLabelText('Image') as HTMLInputElement).value).toBe('nginx:1.27');
  });

  it('Cancel is a router link', async () => {
    const user = await mount();
    expect(screen.getByRole('link', { name: 'Cancel' }).getAttribute('href')).toBe('/');
    await user.click(screen.getByRole('link', { name: 'Cancel' }));
    expect(location()).toBe('/');
  });

  it('posts the request, starts up and navigates to the operation', async () => {
    const user = await mount();
    await fillImage(user);
    expect((screen.getByLabelText('Service name') as HTMLInputElement).value).toBe('web');
    await submit(user);
    await waitFor(() => expect(location()).toBe('/stacks/web?tab=operations&op=op1'));
    expect(post()?.body).toEqual({ name: 'web', services: [{ service: 'web', config: { image: 'nginx:1.27', limits: {} } }] });
    expect(fake.calls.some((c) => c.path === '/api/stacks/web/actions/up')).toBe(true);
  });

  it('with "Start now" unchecked no action is started', async () => {
    const user = await mount();
    await fillImage(user);
    await user.click(screen.getByLabelText(/Start now/));
    await submit(user);
    await waitFor(() => expect(location()).toBe('/stacks/web?tab=operations'));
    expect(fake.calls.some((c) => c.path.includes('/actions/'))).toBe(false);
  });

  it('an edited service name is kept and sent', async () => {
    const user = await mount();
    await fillImage(user);
    const svc = screen.getByLabelText('Service name');
    await user.clear(svc);
    await user.type(svc, 'app');
    await submit(user);
    await waitFor(() => expect(post()).toBeTruthy());
    expect(post()?.body).toMatchObject({ name: 'web', services: [{ service: 'app' }] });
  });

  it('client validation blocks the request and focuses the first error', async () => {
    const user = await mount();
    await user.type(screen.getByLabelText('Stack name'), 'Bad Name');
    await submit(user);
    const field = screen.getByLabelText('Stack name');
    await waitFor(() => expect(document.activeElement).toBe(field));
    expect(field.getAttribute('aria-invalid')).toBe('true');
    expect(post()).toBeUndefined();
  });

  it('409 puts the message under the stack name, not the service name', async () => {
    const user = await mount({ 'POST /api/stacks': { status: 409, body: { error: 'stack already exists' } } });
    await fillImage(user);
    await submit(user);
    expect(await screen.findByText('stack already exists')).toBeTruthy();
    const stack = screen.getByLabelText('Stack name');
    await waitFor(() => expect(document.activeElement).toBe(stack));
    expect(screen.getByLabelText('Service name').getAttribute('aria-invalid')).toBeNull();
    expect(location()).toBe('/stacks/new');
  });

  it('422 shows the compose error verbatim under the form', async () => {
    const user = await mount({ 'POST /api/stacks': { status: 422, body: { error: 'services.web.image: invalid\nline two' } } });
    await fillImage(user);
    await submit(user);
    const alert = await screen.findByText(/services\.web\.image: invalid/);
    expect(alert.textContent).toBe('services.web.image: invalid\nline two');
  });

  it('400 on build.context lands on the Build context field', async () => {
    const user = await mount({ 'POST /api/stacks': { status: 400, body: { error: 'invalid', fields: [{ field: 'services[0].build.context', message: 'path is outside the allowed roots' }] } } });
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await user.click(screen.getByLabelText('From Dockerfile'));
    await user.type(screen.getByLabelText('Build context'), '/etc');
    await submit(user);
    expect(await screen.findByText('path is outside the allowed roots')).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Build context')));
  });

  it('403 (filesystem permission) is shown under the form', async () => {
    const user = await mount({ 'POST /api/stacks': { status: 403, body: { error: 'permission denied' } } });
    await fillImage(user);
    await submit(user);
    const alert = await screen.findByText('permission denied');
    expect(alert.getAttribute('role')).toBe('alert');
  });

  it('a server image error on a build card stays on the Image tag field through other edits and is dropped by a source switch', async () => {
    const user = await mount({ 'POST /api/stacks': { status: 400, body: { error: 'invalid', fields: [{ field: 'services[0].image', message: 'bad tag' }] } } });
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await user.click(screen.getByLabelText('From Dockerfile'));
    await user.type(screen.getByLabelText('Build context'), '/srv/app');
    await user.type(screen.getByLabelText('Image tag'), 'x');
    await submit(user);
    await screen.findByText('bad tag');
    await user.type(screen.getByLabelText('Build context'), '2');
    expect(screen.getByText('bad tag')).toBeTruthy();
    await user.click(screen.getByLabelText('From Image'));
    expect(screen.getByText('bad tag')).toBeTruthy(); // the Image field is the error's own field
    await user.click(screen.getByLabelText('From Dockerfile'));
    expect(screen.queryByText('bad tag')).toBeNull();
  });

  // user-event's Enter only looks for a submit button inside the form, but a browser also counts a button whose `form`
  // attribute names it, so Enter in the Stack name input is pinned by the association the browser uses.
  it('Create is the default submit button of the form that holds the Stack name input, although it sits outside it', async () => {
    await mount();
    const create = screen.getByRole('button', { name: 'Create stack' }) as HTMLButtonElement;
    const stackName = screen.getByLabelText('Stack name') as HTMLInputElement;
    const form = stackName.form as HTMLFormElement;
    const defaultButton = [...form.elements].find((el) => (el as HTMLButtonElement).type === 'submit');
    expect(defaultButton).toBe(create);
    expect(form.contains(create)).toBe(false);
  });

  it('a stale build error is dropped when switching back to image', async () => {
    const user = await mount({ 'POST /api/stacks': { status: 400, body: { error: 'invalid', fields: [{ field: 'services[0].build.context', message: 'bad context' }] } } });
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
    const user = await mount({
      'GET /api/fs': { body: [{ name: '/', isDir: true, size: 0 }] },
      'GET /api/fs?path=%2F': { body: [{ name: 'Dockerfile', isDir: false, size: 5 }] },
    });
    await user.click(screen.getByLabelText('From Dockerfile'));
    await user.type(screen.getByLabelText('Build context'), '/');
    await user.click(screen.getByRole('button', { name: 'Browse for Dockerfile' }));
    await user.click(await screen.findByRole('button', { name: '/' }));
    await user.click(await screen.findByRole('button', { name: /^Dockerfile/ }));
    await waitFor(() => expect((screen.getByLabelText('Dockerfile') as HTMLInputElement).value).toBe('Dockerfile'));
  });

  it('400 field errors land on their fields', async () => {
    const user = await mount({ 'POST /api/stacks': { status: 400, body: { error: 'invalid', fields: [{ field: 'services[0].ports[0]', message: 'port in use' }] } } });
    await fillImage(user);
    await user.click(screen.getByRole('button', { name: 'Add port' }));
    await user.type(screen.getByLabelText('Port 1'), '8080:80');
    await submit(user);
    expect(await screen.findByText('port in use')).toBeTruthy();
  });

  it('503 shows the unavailable banner and stays on the page', async () => {
    const user = await mount({ 'POST /api/stacks': { status: 503, body: { error: 'docker unavailable' } } });
    await fillImage(user);
    await submit(user);
    const banner = await screen.findByText(/the stack was not created/);
    await waitFor(() => expect(document.activeElement?.contains(banner)).toBe(true));
    expect(location()).toBe('/stacks/new');
  });

  it('a partial-checks response raises a warning toast', async () => {
    const user = await mount({ 'POST /api/stacks': { status: 201, body: SUMMARY, headers: { 'X-Stackstead-Checks': 'partial' } } });
    await fillImage(user);
    await submit(user);
    await waitFor(() => expect(location()).toBe('/stacks/web?tab=operations&op=op1'));
    expect(get(toasts).some((t) => t.kind === 'warning')).toBe(true);
  });

  it('a picked Dockerfile inside the context is stored relative to it', async () => {
    const user = await mount();
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await user.click(screen.getByLabelText('From Dockerfile'));
    await user.type(screen.getByLabelText('Build context'), '/srv/app');
    await user.click(screen.getByRole('button', { name: 'Browse for Dockerfile' }));
    await user.click(await screen.findByRole('button', { name: '/srv/app' }));
    await user.click(await screen.findByRole('button', { name: /^build\.Dockerfile/ }));
    await waitFor(() => expect((screen.getByLabelText('Dockerfile') as HTMLInputElement).value).toBe('build.Dockerfile'));
  });

  // Cases new to the React screen.
  it('tracks the up operation so its toast resolves', async () => {
    const user = await mount();
    const refreshesBefore = get(operationsRefresh);
    await fillImage(user);
    await submit(user);
    await waitFor(() => expect(location()).toBe('/stacks/web?tab=operations&op=op1'));
    expect(get(toasts).some((t) => t.message.includes('up · web'))).toBe(true);
    // the running-operations list is re-checked once after create and once after starting up
    expect(get(operationsRefresh) - refreshesBefore).toBeGreaterThanOrEqual(2);
  });

  it('Stack created, but starting it failed is toasted when up is refused', async () => {
    const user = await mount({ 'POST /api/stacks/web/actions/up': { status: 409, body: { error: 'busy' } } });
    await fillImage(user);
    await submit(user);
    await waitFor(() => expect(location()).toBe('/stacks/web?tab=operations'));
    expect(get(toasts).some((t) => t.message === 'Stack created, but starting it failed: busy')).toBe(true);
  });

  it('states what is written and that nothing is written before confirming', async () => {
    const user = await mount();
    const lead = () => document.querySelector('header p')?.textContent;
    expect(lead()).toBe('Writes compose.yaml into a folder named my-stack in the stacks directory. Nothing is written until you confirm.');
    await user.type(screen.getByLabelText('Stack name'), 'blog');
    expect(lead()).toBe('Writes compose.yaml into a folder named blog in the stacks directory. Nothing is written until you confirm.');
  });

  it('does not navigate to the new stack when the user left the page while the create was in flight', async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => (release = resolve));
    routes({ 'POST /api/stacks': async () => { await gate; return { status: 201, body: SUMMARY }; } });
    const mounted = mountAt('/stacks/new');
    await screen.findByLabelText('Stack name');
    const user = userEvent.setup();
    await fillImage(user);
    await submit(user);
    await waitFor(() => expect(post()).toBeTruthy());
    await mounted.goTo('/');
    expect(location()).toBe('/');
    release();
    await waitFor(() => expect(get(toasts).some((t) => t.message === 'Stack web created')).toBe(true));
    expect(location()).toBe('/');
  });

  it('the route table titles /stacks/new as New stack', () => {
    expect(findRoute('/stacks/new')?.title).toBe('New stack');
  });

  it('the first card renders the disclosure closed and the restart policy in view', async () => {
    await mount();
    expect((screen.getByText('Environment, networks, limits, healthcheck').closest('details') as HTMLDetailsElement).open).toBe(false);
    expect(screen.getByLabelText('Restart policy')).toBeTruthy();
  });

  it('a network chosen in the card picker is sent with the service', async () => {
    const net = { id: 'f', name: 'frontend', driver: 'bridge', scope: 'local', subnets: [], gateways: [], ipRange: '', parent: '', ipvlanMode: '', internal: false, builtin: false, project: '', stack: '', containers: [] };
    const user = await mount({ 'GET /api/networks': { body: [net] } });
    await fillImage(user);
    await user.click(screen.getByText('Environment, networks, limits, healthcheck'));
    await user.click(screen.getByRole('button', { name: 'Add network' }));
    await screen.findByRole('option', { name: 'frontend' });
    await user.selectOptions(screen.getByLabelText('Network'), 'frontend');
    await submit(user);
    await waitFor(() => expect(post()).toBeTruthy());
    expect(JSON.stringify(post()?.body)).toContain('frontend');
  });
});
