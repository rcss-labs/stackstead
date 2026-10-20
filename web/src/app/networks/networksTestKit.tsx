// Shared fixtures and routes for the Networks screen tests. Each test file declares its own
// vi.mock('$lib/stores/live', ...) exposing `__live` and `__changes` writables (vi.mock is hoisted per file).
import { screen } from '@testing-library/react';
import type { Network } from '$lib/api/types';
import type { FakeResponse, Routes } from '../../vitest-setup';

export { mountAt } from '../containers/containersTestKit';

export const row = (name: string, over: Partial<Network> = {}): Network => ({
  id: name,
  name,
  driver: 'bridge',
  scope: 'local',
  subnets: [],
  gateways: [],
  ipRange: '',
  parent: '',
  ipvlanMode: '',
  internal: false,
  builtin: false,
  project: '',
  stack: '',
  containers: [],
  ...over,
});

export function routes(response: FakeResponse | (() => FakeResponse)): Routes {
  return {
    'GET /api/networks': response,
    'GET /api/host/interfaces': { body: [] },
    'POST /api/networks': { status: 201, body: row('new') },
  };
}

export const cards = () => screen.getAllByRole('article');
