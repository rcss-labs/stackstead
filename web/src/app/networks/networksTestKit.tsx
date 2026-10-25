// Shared fixtures and routes for the Networks screen tests. Each test file declares its own
// vi.mock('$lib/stores/live', ...) exposing `__live` and `__changes` writables (vi.mock is hoisted per file).
import type { Network, NetworkContainer } from '$lib/api/types';
import type { FakeResponse, RouteHandler, Routes } from '../../vitest-setup';

export { mountAt } from '../containers/containersTestKit';

export const member = (name: string, over: Partial<NetworkContainer> = {}): NetworkContainer => ({
  id: `${name}-id`,
  name,
  ipv4: '',
  mac: '',
  stack: '',
  state: 'running',
  health: '',
  exitCode: null,
  ...over,
});

export const row = (name: string, over: Partial<Network> = {}): Network => ({
  id: name,
  fullId: name,
  name,
  driver: 'bridge',
  scope: 'local',
  subnets: [],
  gateways: [],
  ipRange: '',
  parent: '',
  ipvlanMode: '',
  internal: false,
  attachable: false,
  ipv6: false,
  created: '',
  builtin: false,
  project: '',
  stack: '',
  containers: [],
  ...over,
});

export function routes(response: FakeResponse | RouteHandler, extra: Routes = {}): Routes {
  return {
    'GET /api/networks': response,
    'GET /api/host/interfaces': { body: [] },
    'POST /api/networks': { status: 201, body: row('new') },
    ...extra,
  };
}
