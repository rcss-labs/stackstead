// Shared fixtures and helpers for the New stack screen tests (test-only).
// Each test file declares its own vi.mock('$lib/stores/live', …) exactly as containers.test.tsx does, because
// useContainerList subscribes to `changes`; vi.mock is hoisted per file and cannot be shared from here.
import { screen } from '@testing-library/react';
import type userEvent from '@testing-library/user-event';
import { installFakeFetch, type FakeFetch } from '../../vitest-setup';

export { mountAt } from '../containers/containersTestKit';

export type User = ReturnType<typeof userEvent.setup>;

export const SUMMARY = { name: 'web', path: '/srv/stacks/web/compose.yaml', project: 'web', origin: 'managed', status: 'stopped', serviceCount: 1, running: 0 };
export const OP = { id: 'op1', stack: 'web', action: 'up', state: 'running', startedAt: '2026-10-14T10:00:00Z' };

export let fake: FakeFetch;

/** Installs the fixture set; `extra` entries override the defaults. */
export function routes(extra: Record<string, unknown> = {}): FakeFetch {
  fake = installFakeFetch({
    'GET /api/networks': { body: [] },
    'GET /api/containers': { body: [] },
    'GET /api/stacks': { body: [] },
    'GET /api/operations?state=running': { body: [] },
    'POST /api/stacks': { status: 201, body: SUMMARY },
    'POST /api/stacks/web/actions/up': { status: 202, body: OP },
    'GET /api/fs': { body: [{ name: '/srv/app', isDir: true, size: 0 }] },
    'GET /api/fs?path=%2Fsrv%2Fapp': { body: [{ name: 'Dockerfile', isDir: false, size: 5 }, { name: 'build.Dockerfile', isDir: false, size: 5 }] },
    ...(extra as object),
  } as never);
  return fake;
}

export async function fillImage(user: User, name = 'web') {
  await user.type(screen.getByLabelText('Stack name'), name);
  await user.type(screen.getByLabelText('Image'), 'nginx:1.27');
}

export async function addImage(user: User, name = 'db') {
  await user.click(screen.getByRole('button', { name: '+ Add another service' }));
  await user.type(screen.getByLabelText('Service name'), name);
  await user.type(screen.getByLabelText('Image'), 'postgres:17');
}

export const submit = (user: User) => user.click(screen.getByRole('button', { name: 'Create stack' }));
export const post = () => fake.calls.find((c) => c.method === 'POST' && c.path === '/api/stacks');
export const location = () => screen.getByTestId('location').textContent;
