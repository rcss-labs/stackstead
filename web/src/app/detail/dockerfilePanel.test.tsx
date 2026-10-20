import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { DockerfileView, Operation, ServiceDetail } from '$lib/api/types';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, type FakeFetch, type FakeResponse } from '../../vitest-setup';
import { DockerfilePanel } from './DockerfilePanel';
import { OperationsList } from './OperationsList';
import { ServicePane } from './ServicePane';

// Keep the plain textarea: the CodeMirror chunk is exercised in the YamlEditor and DiffView tests.
vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));

const VIEW: DockerfileView = { content: 'FROM alpine:3.20\n', hash: 'd1', exists: true, editable: true, path: 'web/Dockerfile' };
const DF = '/api/stacks/web/services/web/dockerfile';

let fake: FakeFetch;
let getRes: FakeResponse;
let putRes: FakeResponse;

beforeEach(() => {
  toasts.reset();
  getRes = { body: VIEW };
  putRes = { body: { hash: 'd2' } };
  fake = installFakeFetch({
    [`GET ${DF}`]: () => getRes,
    [`PUT ${DF}`]: () => putRes,
    'POST /api/stacks/web/services/web/actions/up': { body: { id: 'op9', stack: 'web', action: 'up', service: 'web', state: 'running', startedAt: '2026-01-01T00:00:00Z' } },
    'GET /api/operations?state=running': { body: [] },
  });
});
afterEach(async () => {
  cleanup();
  fake.restore();
  await new Promise((r) => setTimeout(r, 5));
});

function Where() {
  const l = useLocation();
  return <output data-testid="where">{l.pathname + l.search}</output>;
}
const mount = () =>
  render(
    <MemoryRouter>
      <DockerfilePanel stackName="web" service="web" />
      <Where />
    </MemoryRouter>,
  );
const editor = () => screen.findByRole('textbox', { name: 'Dockerfile for web' }) as Promise<HTMLTextAreaElement>;
const edit = (ta: HTMLTextAreaElement, v: string) => fireEvent.change(ta, { target: { value: v } });
const puts = () => fake.calls.filter((c) => c.method === 'PUT');
const click = (name: string) => userEvent.click(screen.getByRole('button', { name }));

const svc = (config: ServiceDetail['config'], readOnly: ServiceDetail['readOnly'] = []): ServiceDetail => ({ name: 'web', config, readOnly, containers: [] });

describe('Dockerfile tab', () => {
  it('shows the Dockerfile tab only for services with a build', () => {
    const pane = (service: ServiceDetail) =>
      render(
        <MemoryRouter>
          <ServicePane service={service} tab="overview" onTab={() => {}} onLogs={() => {}} latestCpu={undefined} cpuHour={undefined} />
        </MemoryRouter>,
      );
    const a = pane(svc({ image: 'nginx:1', limits: {} }));
    expect(screen.queryByRole('tab', { name: 'Dockerfile' })).toBeNull();
    a.unmount();
    const b = pane(svc({ build: { context: '.' }, limits: {} }));
    expect(screen.getByRole('tab', { name: 'Dockerfile' })).toBeTruthy();
    b.unmount();
    pane(svc({ limits: {} }, ['build']));
    expect(screen.getByRole('tab', { name: 'Dockerfile' })).toBeTruthy();
  });

  it('loads and saves with the loaded hash', async () => {
    mount();
    const ta = await editor();
    await waitFor(() => expect(ta.value).toBe('FROM alpine:3.20\n'));
    expect(screen.getByText('web/Dockerfile')).toBeTruthy();
    edit(ta, 'FROM alpine:3.21\n');
    await click('Save');
    await waitFor(() => expect(puts()).toHaveLength(1));
    expect(puts()[0].body).toEqual({ content: 'FROM alpine:3.21\n', hash: 'd1' });
  });

  it('Save & rebuild saves then starts the scoped up', async () => {
    mount();
    const ta = await editor();
    await waitFor(() => expect(ta.value).toBe('FROM alpine:3.20\n'));
    edit(ta, 'FROM alpine:3.21\n');
    await click('Save & rebuild');
    await waitFor(() => expect(screen.getByTestId('where').textContent).toBe('/stacks/web?tab=operations&op=op9'));
    const order = fake.calls.filter((c) => c.method !== 'GET').map((c) => `${c.method} ${c.path}`);
    expect(order.slice(0, 2)).toEqual([`PUT ${DF}`, 'POST /api/stacks/web/services/web/actions/up']);
  });

  it('a conflict offers Reload and Discard', async () => {
    putRes = { status: 409, body: { error: 'Dockerfile changed on disk since it was loaded' } };
    mount();
    const ta = await editor();
    await waitFor(() => expect(ta.value).toBe('FROM alpine:3.20\n'));
    edit(ta, 'FROM alpine:3.21\n');
    await click('Save');
    expect(await screen.findByText('Dockerfile changed on disk since it was loaded')).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Reload' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Discard my changes' })).toBeTruthy();
  });

  it('an outside Dockerfile is read-only with the note', async () => {
    getRes = { body: { ...VIEW, editable: false, path: '/etc/Dockerfile', reason: 'outside the stack folder' } };
    mount();
    const ta = await editor();
    expect(await screen.findByText("This Dockerfile is outside the stack's folder and can't be edited here.")).toBeTruthy();
    expect(ta.readOnly).toBe(true);
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Save & rebuild' })).toBeNull();
  });

  it('a not-editable Dockerfile names the reason', async () => {
    getRes = { body: { ...VIEW, editable: false, reason: 'uses variables' } };
    mount();
    expect(await screen.findByText("This Dockerfile can't be edited here: uses variables")).toBeTruthy();
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
  });

  it('a missing Dockerfile starts from the template and Save creates it with an empty hash', async () => {
    getRes = { body: { content: '', hash: '', exists: false, editable: true, path: 'web/Dockerfile' } };
    mount();
    const ta = await editor();
    await waitFor(() => expect(ta.value).toMatch(/^FROM alpine:3\.20/));
    await click('Save');
    await waitFor(() => expect(puts()).toHaveLength(1));
    expect((puts()[0].body as { hash: string }).hash).toBe('');
  });
});

describe('Dockerfile panel states', () => {
  it('does not inject the template into a read-only view and hides the empty editor', async () => {
    getRes = { body: { content: '', hash: '', exists: false, editable: false, path: '/etc/Dockerfile', reason: 'outside the allowed directories' } };
    mount();
    expect(await screen.findByText(/outside the stack's folder and can't be edited here\./)).toBeTruthy();
    expect(screen.getByText(/content isn't shown/)).toBeTruthy();
    expect(screen.queryByDisplayValue(/FROM alpine/)).toBeNull();
    expect(screen.queryByRole('textbox', { name: 'Dockerfile for web' })).toBeNull();
  });

  it('an untouched new-file template is not dirty, an edit is', async () => {
    getRes = { body: { content: '', hash: '', exists: false, editable: true, path: 'web/Dockerfile' } };
    const seen: boolean[] = [];
    render(
      <MemoryRouter>
        <DockerfilePanel stackName="web" service="web" onDirtyChange={(d) => seen.push(d)} />
      </MemoryRouter>,
    );
    const ta = await editor();
    await waitFor(() => expect(ta.value).toMatch(/^FROM alpine/));
    expect(seen.includes(true)).toBe(false);
    edit(ta, 'FROM alpine:3.21\n');
    await waitFor(() => expect(seen.at(-1)).toBe(true));
  });
});

describe('operations list', () => {
  it('operations list names the rebuilt service', () => {
    const op: Operation = { id: 'o', stack: 'web', action: 'up', service: 'web', state: 'succeeded', startedAt: '2026-01-01T00:00:00Z' };
    render(
      <MemoryRouter>
        <OperationsList operations={[op]} href={(id) => `/x/${id}`} />
      </MemoryRouter>,
    );
    expect(screen.getByText('docker compose up --build web')).toBeTruthy();
  });
});
