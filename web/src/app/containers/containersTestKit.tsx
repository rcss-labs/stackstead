// Shared fixtures and a router mount for the containers screen tests.
import { act, render } from '@testing-library/react';
import { Suspense } from 'react';
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from 'react-router';
import type { ContainerDetail, ContainerRow } from '$lib/api/types';
import { ROUTES } from '../routes';

export const row = (name: string, over: Partial<ContainerRow> = {}): ContainerRow => ({
  id: name.padEnd(12, '0').slice(0, 12),
  name,
  image: 'nginx:1',
  imageId: '',
  state: 'running',
  status: 'Up 1 hour',
  health: 'none',
  exitCode: null,
  createdAt: '2026-01-01T00:00:00Z',
  project: '',
  service: '',
  stack: '',
  managed: 'none',
  ports: [],
  networks: {},
  cpu: null,
  mem: null,
  memLimit: null,
  restarts24h: 0,
  ...over,
});

const FULL_ID = 'abc123def456' + '0'.repeat(52);

export const detail = (over: Partial<ContainerDetail> = {}): ContainerDetail => ({
  id: FULL_ID,
  name: 'redis',
  image: 'redis:7',
  imageId: '',
  state: 'running',
  status: 'Up 1 hour',
  health: 'none',
  startedAt: '2026-10-15T08:00:00Z',
  finishedAt: null,
  exitCode: null,
  restartPolicy: 'unless-stopped',
  restarts24h: 2,
  project: '',
  service: '',
  stack: '',
  managed: 'none',
  ports: ['6379/tcp'],
  networks: { bridge: '172.17.0.2' },
  mounts: [{ type: 'volume', name: 'redis-data', source: 'redis-data', destination: '/data', readOnly: false }],
  labels: { 'com.example.owner': 'me', 'com.docker.compose.project': 'shop' },
  memoryLimit: null,
  ...over,
});

export interface Mounted {
  goTo: (href: string) => void;
}

/** The app's route table with `/` stubbed; `goTo` navigates the mounted router. */
export function mountAt(path: string) {
  const mounted: Mounted = { goTo: () => {} };
  function Probe() {
    const location = useLocation();
    const navigate = useNavigate();
    mounted.goTo = (href) => act(() => void navigate(href));
    return <output data-testid="location">{location.pathname + location.search}</output>;
  }
  const view = render(
    <MemoryRouter initialEntries={[path]}>
      <Suspense fallback={<p>loading page</p>}>
        <Routes>
          {ROUTES.map(({ path: pattern, screen: Screen }) => (
            <Route key={pattern} path={pattern} element={pattern === '/' ? <p>home</p> : <Screen />} />
          ))}
        </Routes>
      </Suspense>
      <Probe />
    </MemoryRouter>,
  );
  return { ...view, ...mounted, goTo: (href: string) => mounted.goTo(href) };
}
