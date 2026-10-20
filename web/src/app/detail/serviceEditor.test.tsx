import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import type { ServiceDetail, StackDetail } from '$lib/api/types';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeResponse, type FakeStream, type RecordedCall } from '../../vitest-setup';

// Keep the plain textarea and fallback diff: the CodeMirror chunk is exercised in the YamlEditor and DiffView tests.
vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));
vi.mock('$lib/stores/live', async (importActual) => ({ ...(await importActual<object>()), refreshStatus: vi.fn(async () => {}) }));
vi.mock('$lib/stores/stacks', async (importActual) => ({
  ...(await importActual<object>()),
  refreshStacks: vi.fn(async () => {}),
  refreshRunningOps: vi.fn(),
}));

import { refreshStatus } from '$lib/stores/live';
import { refreshRunningOps, refreshStacks } from '$lib/stores/stacks';
import { ServiceEditor, type ServiceEditorProps } from './ServiceEditor';

const svc: ServiceDetail = {
  name: 'app',
  config: { image: 'nginx:1', ports: ['80:80'], limits: {} },
  readOnly: [],
  containers: [],
};
const stack = (hash = 'h1', s: ServiceDetail = svc): StackDetail => ({
  name: 'web', path: '/s/web', project: 'web', origin: 'managed', status: 'running', serviceCount: 1, running: 1, health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 }, ports: [], hash, services: [s],
});
const op = (state: string) => ({ id: 'op1', stack: 'web', action: 'up', state, startedAt: '2026-01-01T00:00:00Z', exitCode: state === 'succeeded' ? 0 : undefined });

let fake: FakeFetch;
let stream: FakeStream | undefined;
let previewRes: FakeResponse[];
let putRes: FakeResponse[];
let composeHash: string;

beforeEach(() => {
  toasts.reset();
  vi.mocked(refreshStacks).mockClear();
  vi.mocked(refreshRunningOps).mockClear();
  vi.mocked(refreshStatus).mockClear();
  composeHash = 'h1';
  previewRes = [];
  putRes = [];
  fake = installFakeFetch({
    'GET /api/stacks/web/compose': () => ({ body: { content: 'services:\n  app:\n    image: nginx:1\n', hash: composeHash } }),
    'POST /api/stacks/web/services/app/preview': (c) =>
      previewRes.shift() ?? { body: { content: 'services:\n  app:\n    image: PREVIEWED\n', hash: (c.body as { hash: string }).hash } },
    'PUT /api/stacks/web/services/app': () => putRes.shift() ?? { body: { hash: 'h2' } },
    'GET /api/networks': { body: [] },
    'POST /api/stacks/web/actions/up': { body: op('running') },
    'GET /api/operations/op1': { body: { ...op('running'), lines: [] } },
  });
});
afterEach(async () => {
  cleanup();
  stream?.restore();
  stream = undefined;
  fake.restore();
  await new Promise((r) => setTimeout(r, 5)); // useStore releases its subscriptions on the next task
});

const calls = (method: string, suffix: string): RecordedCall[] => fake.calls.filter((c) => c.method === method && c.path.endsWith(suffix));
function mount(props: Partial<ServiceEditorProps> = {}) {
  const handlers = {
    onSaved: vi.fn(),
    onRawEditor: vi.fn(),
    onReload: vi.fn(async () => stack('h9', { ...svc, config: { ...svc.config, restart: 'always' } })),
  };
  render(<ServiceEditor stack={stack()} service={svc} {...handlers} {...props} />);
  return handlers;
}
const user = () => userEvent.setup();
async function changeImage(value: string) {
  const image = screen.getByLabelText('Image');
  await user().clear(image);
  if (value) await user().type(image, value);
}

describe('ServiceEditor', () => {
  it('prefills the image tag when switching to Write a Dockerfile', async () => {
    mount({ stack: stack('h1', { ...svc, name: 'web' }), service: { ...svc, name: 'web' } });
    await user().click(screen.getByLabelText('Write a Dockerfile'));
    expect((screen.getByLabelText('Image tag') as HTMLInputElement).value).toBe('stackstead/web:latest');
    expect(screen.getByLabelText('Dockerfile for web')).toBeTruthy();
  });

  it('loads an editable Dockerfile and saves its change with the loaded hash', async () => {
    const built: ServiceDetail = { ...svc, config: { image: 'stackstead/web-app:latest', build: { context: './app' }, limits: {} } };
    fake.restore();
    fake = installFakeFetch({
      'GET /api/stacks/web/compose': { body: { content: 'x', hash: 'h1' } },
      'GET /api/stacks/web/services/app/dockerfile': { body: { content: 'FROM alpine:3.20\n', hash: 'd1', exists: true, editable: true, path: 'app/Dockerfile' } },
      'POST /api/stacks/web/services/app/preview': (c) => ({ body: { content: 'x', hash: (c.body as { hash: string }).hash } }),
      'PUT /api/stacks/web/services/app': { body: { hash: 'h2' } },
      'GET /api/networks': { body: [] },
    });
    mount({ stack: stack('h1', built), service: built });
    const editor = (await screen.findByLabelText('Dockerfile for app')) as HTMLTextAreaElement;
    expect(editor.value).toBe('FROM alpine:3.20\n');
    await user().type(editor, '# note');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await user().click(await screen.findByRole('button', { name: 'Save' }));
    await waitFor(() => expect(calls('PUT', '/services/app')).toHaveLength(1));
    const put = calls('PUT', '/services/app')[0].body as { dockerfile: { content: string; hash: string } };
    expect(put.dockerfile).toEqual({ content: 'FROM alpine:3.20\n# note', hash: 'd1' });
    expect((calls('POST', '/preview')[0].body as Record<string, unknown>).dockerfile).toBeUndefined();
  });

  it('shows an outside Dockerfile as a read-only build with its note', async () => {
    const built: ServiceDetail = { ...svc, config: { build: { context: '/elsewhere' }, limits: {} } };
    fake.restore();
    fake = installFakeFetch({
      'GET /api/stacks/web/services/app/dockerfile': { body: { content: '', hash: '', exists: true, editable: false, path: '/elsewhere/Dockerfile' } },
      'GET /api/networks': { body: [] },
    });
    mount({ stack: stack('h1', built), service: built });
    expect(await screen.findByText("This Dockerfile is outside the stack's folder and can't be edited here.")).toBeTruthy();
    expect(screen.queryByLabelText('Dockerfile for app')).toBeNull();
  });

  it('previews the diff, saves with the same hash and then offers Apply', async () => {
    const h = mount();
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    expect(await screen.findByTestId('diff-view')).toBeTruthy();
    expect(screen.getByTestId('diff-view').textContent).toContain('PREVIEWED');
    const heading = screen.getByRole('heading', { name: 'Review changes to app' });
    await waitFor(() => expect(document.activeElement).toBe(heading));
    const pv = calls('POST', '/preview')[0].body as { config: { image: string }; hash: string };
    expect(pv.config.image).toBe('nginx:2');
    expect(pv.hash).toBe('h1');
    expect(screen.queryByRole('button', { name: 'Apply' })).toBeNull();

    await user().click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(h.onSaved).toHaveBeenCalledWith('h2'));
    expect((calls('PUT', '/services/app')[0].body as { hash: string }).hash).toBe('h1');
    expect(await screen.findByRole('button', { name: 'Apply' })).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'Saved' })));
    expect(get(toasts).some((t) => t.message === 'Saved app. Apply to run the change.')).toBe(true);
  });

  it('validation errors block the preview request and focus the first invalid field', async () => {
    mount();
    await changeImage('');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Image')));
    expect(screen.getByLabelText('Image').getAttribute('aria-invalid')).toBe('true');
    expect(calls('POST', '/preview')).toHaveLength(0);
  });

  it('server field errors land on the right input and take focus', async () => {
    previewRes = [{ status: 400, body: { error: 'invalid', fields: [{ field: 'services.app.image', message: 'image not allowed' }] } }];
    mount();
    await changeImage('bad');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await waitFor(() => expect(screen.getByLabelText('Image').getAttribute('aria-invalid')).toBe('true'));
    expect(screen.getByText(/image not allowed/)).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText('Image')));
  });

  it('a 422 "needs the raw YAML editor" offers the raw editor and keeps the form', async () => {
    putRes = [{ status: 422, body: { error: 'this change needs the raw YAML editor' } }];
    const h = mount();
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await user().click(await screen.findByRole('button', { name: 'Save' }));
    expect((await screen.findByRole('alert')).textContent).toContain('this change needs the raw YAML editor');
    expect(screen.getByText('The form cannot make this change')).toBeTruthy();
    await user().click(screen.getByRole('button', { name: 'Open raw YAML editor' }));
    expect(h.onRawEditor).toHaveBeenCalledOnce();
    expect((screen.getByLabelText('Image') as HTMLInputElement).value).toBe('nginx:2');
  });

  it('a 409 offers Reload, which keeps the user form values, takes the new hash and re-previews', async () => {
    putRes = [{ status: 409, body: { error: 'compose file changed on disk' } }];
    const h = mount();
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await user().click(await screen.findByRole('button', { name: 'Save' }));
    expect((await screen.findByRole('alert')).textContent).toContain('compose file changed on disk');
    expect(screen.getByText('Save refused')).toBeTruthy();
    composeHash = 'h9';
    await user().click(screen.getByRole('button', { name: 'Reload' }));
    await waitFor(() => expect(h.onReload).toHaveBeenCalledOnce());
    await waitFor(() => expect(calls('POST', '/preview')).toHaveLength(2));
    expect((calls('POST', '/preview')[1].body as { hash: string; config: { image: string } }).hash).toBe('h9');
    expect((calls('POST', '/preview')[1].body as { config: { image: string } }).config.image).toBe('nginx:2');
    expect(await screen.findByRole('button', { name: 'Save' })).toBeTruthy();
  });

  it('Discard my changes reseeds the form from the reloaded stack', async () => {
    putRes = [{ status: 409, body: { error: 'changed' } }];
    mount({ onReload: vi.fn(async () => stack('h9', { ...svc, config: { ...svc.config, image: 'nginx:7' } })) });
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await user().click(await screen.findByRole('button', { name: 'Save' }));
    await user().click(await screen.findByRole('button', { name: 'Discard my changes' }));
    await waitFor(() => expect((screen.getByLabelText('Image') as HTMLInputElement).value).toBe('nginx:7'));
  });

  it('a 503 is shown as an error and leaves the form editable', async () => {
    previewRes = [{ status: 503, body: { error: 'docker unavailable' } }];
    mount();
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    expect((await screen.findByRole('alert')).textContent).toContain('docker unavailable');
    expect(screen.getByText('Could not complete the request')).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Open raw YAML editor' })).toBeNull();
  });

  it('warns when the checks were partial', async () => {
    putRes = [{ body: { hash: 'h2' }, headers: { 'X-Stackstead-Checks': 'partial' } }];
    mount();
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await user().click(await screen.findByRole('button', { name: 'Save' }));
    await screen.findByRole('button', { name: 'Apply' });
    expect(get(toasts).some((t) => t.kind === 'warning' && /checks were partial/i.test(t.message))).toBe(true);
  });

  it('Apply runs up, shows the operation and refreshes stacks and running ops when it ends', async () => {
    stream = installFakeStream({
      '/api/operations/op1/stream': [`event: line\nid: 0\ndata: "creating"\n\n`, `event: done\ndata: ${JSON.stringify(op('succeeded'))}\n\n`],
    });
    const onApplied = vi.fn();
    mount({ onApplied });
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await user().click(await screen.findByRole('button', { name: 'Save' }));
    await user().click(await screen.findByRole('button', { name: 'Apply' }));
    expect(calls('POST', '/actions/up')).toHaveLength(1);
    expect((await screen.findByRole('log')).textContent).toBeDefined();
    expect(onApplied).toHaveBeenCalledOnce();
    await waitFor(() => expect(refreshStacks).toHaveBeenCalled());
    expect(refreshRunningOps).toHaveBeenCalled();
  });

  it('Apply tracks the operation as a pending toast', async () => {
    stream = installFakeStream({ '/api/operations/op1/stream': [] });
    mount();
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await user().click(await screen.findByRole('button', { name: 'Save' }));
    await user().click(await screen.findByRole('button', { name: 'Apply' }));
    await screen.findByRole('log');
    expect(get(toasts).filter((t) => t.pending).map((t) => t.message)).toEqual(['up · web']);
  });

  it('Apply is disabled with its reason when lifecycle actions are unavailable', async () => {
    mount({ lifecycleDisabledReason: 'Docker Compose is unavailable' });
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await user().click(await screen.findByRole('button', { name: 'Save' }));
    const apply = await screen.findByRole('button', { name: 'Apply' });
    await user().click(apply);
    expect(calls('POST', '/actions/up')).toHaveLength(0);
    expect(document.body.textContent).toContain('Docker Compose is unavailable');
  });

  it('reports dirty while the form differs from the saved config', async () => {
    const onDirtyChange = vi.fn();
    mount({ onDirtyChange });
    await waitFor(() => expect(onDirtyChange).toHaveBeenLastCalledWith(false));
    await changeImage('nginx:2');
    await waitFor(() => expect(onDirtyChange).toHaveBeenLastCalledWith(true));
  });

  const HINT = "This field uses YAML the form can't edit — open the raw YAML editor";

  it('Reload re-applies the new read-only set: note shown, new original ports sent, rebase notice visible', async () => {
    putRes = [{ status: 409, body: { error: 'compose file changed on disk' } }];
    const roSvc: ServiceDetail = { ...svc, config: { ...svc.config, ports: ['8080:80'] }, readOnly: ['ports'] };
    mount({ onReload: vi.fn(async () => stack('h9', roSvc)) });
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await user().click(await screen.findByRole('button', { name: 'Save' }));
    await screen.findByRole('button', { name: 'Reload' });
    composeHash = 'h9';
    await user().click(screen.getByRole('button', { name: 'Reload' }));
    await waitFor(() => expect(calls('POST', '/preview')).toHaveLength(2));
    const body = calls('POST', '/preview')[1].body as { config: { ports: string[]; image: string } };
    expect(body.config.ports).toEqual(['8080:80']);
    expect(body.config.image).toBe('nginx:2');
    expect(document.body.textContent).toContain('The file changed since you opened this editor');
    await user().click(await screen.findByRole('button', { name: 'Back to editing' }));
    expect(screen.getByText(HINT)).toBeTruthy();
  });

  it('read-only groups are shown as such and sent unchanged', async () => {
    const roSvc: ServiceDetail = { ...svc, readOnly: ['ports'] };
    mount({ service: roSvc, stack: stack('h1', roSvc) });
    expect(screen.getByText(HINT)).toBeTruthy();
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await screen.findByTestId('diff-view');
    expect((calls('POST', '/preview')[0].body as { config: { ports: string[] } }).config.ports).toEqual(['80:80']);
  });

  it('a 503 asks the live store to refresh the Docker status', async () => {
    previewRes = [{ status: 503, body: { error: 'docker unavailable' } }];
    mount();
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await screen.findByRole('alert');
    expect(refreshStatus).toHaveBeenCalled();
  });

  it('Apply stays disabled while its operation is running', async () => {
    stream = installFakeStream({ '/api/operations/op1/stream': [`event: line\nid: 0\ndata: "creating"\n\n`] });
    mount();
    await changeImage('nginx:2');
    await user().click(screen.getByRole('button', { name: 'Preview changes' }));
    await user().click(await screen.findByRole('button', { name: 'Save' }));
    await user().click(await screen.findByRole('button', { name: 'Apply' }));
    await screen.findByRole('log');
    await user().click(screen.getByRole('button', { name: 'Apply' }));
    expect(calls('POST', '/actions/up')).toHaveLength(1);
  });
});
