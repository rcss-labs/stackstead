import { cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { toasts } from '$lib/stores/toasts';
import { fake, fillImage, mountAt, post, routes, submit } from './createTestKit';

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

// jsdom cannot lay out CodeMirror; a failing editor chunk leaves the textarea fallback, which tests can type into.
vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('no editor in jsdom');
  },
}));

const serviceCards = () => screen.getAllByRole('region').filter((r) => r.getAttribute('aria-labelledby') !== 'preview-heading');
const yamlBox = () => screen.getByRole('textbox', { name: 'Stack compose file' }) as HTMLTextAreaElement;
const setYaml = (text: string) => fireEvent.change(yamlBox(), { target: { value: text } });
const pickMode = (user: ReturnType<typeof userEvent.setup>, name: 'Form' | 'YAML') =>
  user.click(within(screen.getByRole('radiogroup', { name: 'Editor mode' })).getByRole('radio', { name }));
const createButton = () => screen.getByRole('button', { name: 'Create stack' });

async function mount(extra: Record<string, unknown> = {}) {
  routes(extra);
  mountAt('/stacks/new');
  await screen.findByLabelText('Stack name');
  return userEvent.setup();
}

beforeAll(async () => {
  await import('../routes/StackCreate');
}, 30_000);

afterEach(() => {
  cleanup();
  fake?.restore();
  toasts.reset();
});

const THREE = 'services:\n  web:\n    image: nginx:1.27\n  api:\n    image: node:22\n  db:\n    image: postgres:17\n';

describe('New stack YAML mode', () => {
  it('has an Editor mode radiogroup with Form checked', async () => {
    await mount();
    const group = screen.getByRole('radiogroup', { name: 'Editor mode' });
    expect((within(group).getByRole('radio', { name: 'Form' }) as HTMLInputElement).checked).toBe(true);
    expect(within(group).getByRole('radio', { name: 'YAML' })).toBeTruthy();
  });

  it('switching to YAML shows the cards as one document', async () => {
    const user = await mount();
    await fillImage(user);
    await pickMode(user, 'YAML');
    expect(yamlBox().value).toContain('services:');
    expect(yamlBox().value).toContain('image: nginx:1.27');
  });

  it('switching to YAML from an untouched screen opens an empty services document instead of a name error', async () => {
    const user = await mount();
    await pickMode(user, 'YAML');
    expect(yamlBox().value).toBe('services: {}\n');
    expect(screen.queryByTestId('yaml-error')).toBeNull();
  });

  it('YAML to Form makes one card per service in order', async () => {
    const user = await mount();
    await pickMode(user, 'YAML');
    setYaml(THREE);
    await pickMode(user, 'Form');
    expect(serviceCards().map((c) => c.getAttribute('aria-labelledby')).length).toBe(3);
    expect(screen.getAllByRole('button', { name: /^(web|api|db) / }).map((b) => b.textContent?.split(' ')[0])).toEqual(['web', 'api', 'db']);
    await pickMode(user, 'YAML');
    setYaml('services:\n  web:\n    image: nginx:1.27\n  db:\n    image: postgres:17\n');
    await pickMode(user, 'Form');
    expect(screen.getAllByRole('button', { name: /^(web|api|db) / }).map((b) => b.textContent?.split(' ')[0])).toEqual(['web', 'db']);
  });

  it('a syntax error blocks switching and Create and shows the line', async () => {
    const user = await mount();
    await pickMode(user, 'YAML');
    setYaml('services: {}\nservices: {}\n');
    await pickMode(user, 'Form');
    expect(screen.getByTestId('yaml-error').textContent).toMatch(/^Line 2:/);
    expect((screen.getByRole('radio', { name: 'Form' }) as HTMLInputElement).checked).toBe(false);
    expect(createButton().getAttribute('title')).toBe('Fix the YAML error first');
    expect(createButton().getAttribute('aria-disabled')).toBe('true');
  });

  it('checks the text on each edit, so Create is blocked before any switch and freed by a fix', async () => {
    const user = await mount();
    await pickMode(user, 'YAML');
    setYaml('services: {}\nservices: {}\n');
    await waitFor(() => expect(createButton().getAttribute('title')).toBe('Fix the YAML error first'));
    setYaml(THREE);
    await waitFor(() => expect(screen.queryByTestId('yaml-error')).toBeNull());
    expect(createButton().getAttribute('title')).not.toBe('Fix the YAML error first');
  });

  it('Go to line moves the caret to the error line', async () => {
    const user = await mount();
    await pickMode(user, 'YAML');
    setYaml('services: {}\nservices: {}\n');
    await pickMode(user, 'Form');
    await user.click(screen.getByRole('button', { name: 'Go to line' }));
    expect(yamlBox().selectionStart).toBe('services: {}\n'.length);
  });

  it('missing services and 21 services are refused with the copy', async () => {
    const user = await mount();
    await pickMode(user, 'YAML');
    setYaml('name: x\n');
    await pickMode(user, 'Form');
    expect(screen.getByTestId('yaml-error').textContent).toBe('A stack needs a services: section');
    setYaml('services:\n' + Array.from({ length: 21 }, (_, i) => `  s${i}:\n    image: x\n`).join(''));
    await pickMode(user, 'Form');
    expect(screen.getByTestId('yaml-error').textContent).toBe('A stack can have at most 20 services.');
  });

  it('unknown keys show a settings note and reach the request', async () => {
    const user = await mount();
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await pickMode(user, 'YAML');
    setYaml('services:\n  web:\n    image: nginx:1.27\n    labels:\n      tier: front\n    logging:\n      driver: json-file\nvolumes:\n  data: {}\n');
    await pickMode(user, 'Form');
    expect(screen.getByText('2 more settings from YAML')).toBeTruthy();
    await submit(user);
    await waitFor(() => expect(post()).toBeTruthy());
    const body = post()?.body as { services: { extra: Record<string, unknown> }[]; extra: Record<string, unknown> };
    expect(body.services[0].extra).toEqual({ labels: { tier: 'front' }, logging: { driver: 'json-file' } });
    expect(body.extra).toEqual({ volumes: { data: {} } });
  });

  it('says 1 more setting for a single key', async () => {
    const user = await mount();
    await pickMode(user, 'YAML');
    setYaml('services:\n  web:\n    image: nginx:1.27\n    labels: {a: b}\n');
    await pickMode(user, 'Form');
    expect(screen.getByText('1 more setting from YAML')).toBeTruthy();
  });

  it('Create in YAML mode sends the parsed document and never sends services or name as extra', async () => {
    const user = await mount();
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await pickMode(user, 'YAML');
    setYaml('name: ignored\nservices:\n  web:\n    image: nginx:1.27\nvolumes:\n  data: {}\n');
    await submit(user);
    await waitFor(() => expect(post()).toBeTruthy());
    expect(post()?.body).toEqual({
      name: 'web',
      services: [{ service: 'web', config: { image: 'nginx:1.27', limits: {} } }],
      extra: { volumes: { data: {} } },
    });
  });

  it('a top-level name feeds the stack name only when it is empty', async () => {
    const user = await mount();
    await pickMode(user, 'YAML');
    setYaml('name: shop\nservices:\n  web:\n    image: nginx:1.27\n');
    await pickMode(user, 'Form');
    expect((screen.getByLabelText('Stack name') as HTMLInputElement).value).toBe('shop');
    await pickMode(user, 'YAML');
    setYaml('name: other\nservices:\n  web:\n    image: nginx:1.27\n');
    await pickMode(user, 'Form');
    expect((screen.getByLabelText('Stack name') as HTMLInputElement).value).toBe('shop');
  });

  it('returning to YAML without form edits keeps comments', async () => {
    const user = await mount();
    await fillImage(user);
    await pickMode(user, 'YAML');
    setYaml(`# keep me\n${yamlBox().value}`);
    await pickMode(user, 'Form');
    await pickMode(user, 'YAML');
    expect(yamlBox().value).toContain('# keep me');
  });

  it('a form edit regenerates the YAML without comments', async () => {
    const user = await mount();
    await fillImage(user);
    await pickMode(user, 'YAML');
    setYaml(`# keep me\n${yamlBox().value}`);
    await pickMode(user, 'Form');
    await user.type(screen.getByLabelText('Image'), '-alpine');
    await pickMode(user, 'YAML');
    expect(yamlBox().value).not.toContain('# keep me');
    expect(yamlBox().value).toContain('image: nginx:1.27-alpine');
  });

  it('a long-syntax ports group is read-only on the card with the YAML hint', async () => {
    const user = await mount();
    await pickMode(user, 'YAML');
    setYaml('services:\n  web:\n    image: nginx:1.27\n    ports:\n      - target: 80\n        published: 8080\n');
    await pickMode(user, 'Form');
    expect(screen.getByText('Set in YAML. Switch to YAML to change it.')).toBeTruthy();
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await submit(user);
    await waitFor(() => expect(post()).toBeTruthy());
    expect(post()?.body).toMatchObject({ services: [{ service: 'web', extra: { ports: [{ target: 80, published: 8080 }] } }] });
  });

  it('server field errors in YAML mode are listed by service and field', async () => {
    const user = await mount({
      'POST /api/stacks': { status: 400, body: { error: 'invalid', fields: [{ field: 'services[1].ports[0]', message: 'port in use' }] } },
    });
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await pickMode(user, 'YAML');
    setYaml('services:\n  web:\n    image: nginx:1.27\n  db:\n    image: postgres:17\n    ports:\n      - "5432:5432"\n');
    await submit(user);
    const alert = await screen.findByTestId('yaml-field-errors');
    expect(alert.getAttribute('role')).toBe('alert');
    expect(alert.textContent).toContain('db: Port 1: port in use');
    expect(yamlBox().value).toContain('postgres:17'); // the document stays as typed
  });

  it('client checks on the parsed cards are listed instead of sending the request', async () => {
    const user = await mount();
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await pickMode(user, 'YAML');
    setYaml('services:\n  web: {}\n');
    await submit(user);
    expect((await screen.findByTestId('yaml-field-errors')).textContent).toMatch(/^web: /m);
    expect(post()).toBeUndefined();
  });

  it('Form to YAML is blocked by an empty or duplicate service name, with the name error on the card', async () => {
    const user = await mount();
    await fillImage(user);
    await user.click(screen.getByRole('button', { name: '+ Add another service' }));
    await pickMode(user, 'YAML');
    expect((screen.getByRole('radio', { name: 'Form' }) as HTMLInputElement).checked).toBe(true);
    expect(screen.queryByRole('textbox', { name: 'Stack compose file' })).toBeNull();
    expect(screen.getByText('name is required')).toBeTruthy();
    await user.type(screen.getByLabelText('Service name'), 'web');
    await pickMode(user, 'YAML');
    expect(screen.getByText('duplicate service name web')).toBeTruthy();
    expect(yamlBox).toThrow();
  });

  it('services: {} gives zero cards and Create stays disabled until a service exists', async () => {
    const user = await mount();
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await pickMode(user, 'YAML');
    setYaml('services: {}\n');
    await pickMode(user, 'Form');
    expect(serviceCards()).toHaveLength(0);
    expect(createButton().getAttribute('aria-disabled')).toBe('true');
    await user.click(screen.getByRole('button', { name: '+ Add another service' }));
    await user.type(screen.getByLabelText('Service name'), 'web');
    await user.type(screen.getByLabelText('Image'), 'nginx:1.27');
    expect(createButton().getAttribute('aria-disabled')).toBeNull();
  });

  it('services: {} in YAML keeps Create disabled', async () => {
    const user = await mount();
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await pickMode(user, 'YAML');
    setYaml('services: {}\n');
    await waitFor(() => expect(createButton().getAttribute('aria-disabled')).toBe('true'));
  });

  it('keeps a written Dockerfile through a round trip by service name', async () => {
    const user = await mount();
    await user.type(screen.getByLabelText('Stack name'), 'web');
    await user.click(screen.getByRole('radio', { name: 'Write a Dockerfile' }));
    fireEvent.change(screen.getByRole('textbox', { name: 'Dockerfile for web' }), { target: { value: 'FROM busybox\n' } });
    await pickMode(user, 'YAML');
    expect(yamlBox().value).toContain('build: ./web');
    await pickMode(user, 'Form');
    expect((screen.getByRole('radio', { name: 'Write a Dockerfile' }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole('textbox', { name: 'Dockerfile for web' }) as HTMLTextAreaElement).value).toBe('FROM busybox\n');
  });
});
