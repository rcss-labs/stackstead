import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Suspense } from 'react';
import { MemoryRouter, Route, Routes, useLocation, useNavigate } from 'react-router';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from '$lib/store';
import { unsavedChanges } from '$lib/stores/guard';
import { toasts } from '$lib/stores/toasts';
import { installFakeFetch, installFakeStream } from '../../vitest-setup';

// Keep the plain textarea: the CodeMirror chunk is exercised in the YamlEditor and DiffView tests.
vi.mock('$lib/stacks/cm', () => ({
  createEditor: () => {
    throw new Error('chunk unavailable in this test');
  },
  createDiffView: () => {
    throw new Error('chunk unavailable in this test');
  },
}));

import { ROUTES } from '../routes';
import { localScope, ServerContext } from '../servers/ServerContext';
import { appService, dbService, installHarness, removeHarness, stackDetail, type Harness } from './detailTestKit';

// Load the lazy route chunk up front: on a cold cache, transforming it can outlast the first findBy timeout.
beforeAll(async () => {
  await import('../routes/StackDetail');
}, 30_000);

let h: Harness;
let compose: { content: string; hash: string; hasBackup?: boolean };
let restoreRes: { status?: number; body: unknown };
let goTo: (href: string) => void;

beforeEach(() => {
  stubMobile(false); // the media store keeps its last value when matchMedia disappears, so every test states its width
  h = installHarness();
  h.operations = [];
  compose = { content: 'services: {}\n', hash: 'c1' };
  restoreRes = { body: { hash: 'c2' } };
  h.stream.restore(); // unwind in reverse order of installation
  h.fake.restore();
  h.fake = installFakeFetch({
    'GET /api/stacks/web': () => h.stackRes,
    'GET /api/stacks/web/operations': () => ({ body: h.operations }),
    'GET /api/stacks/web/health?span=60m': { body: { from: 0, to: 3600, services: [] } },
    'GET /api/stacks/web/metrics': { body: { points: [] } },
    'GET /api/stacks/web/compose': () => ({ body: compose }),
    'POST /api/stacks/web/compose/restore-backup': () => restoreRes,
    'GET /api/networks': { body: [] },
    'GET /api/status': { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
    'GET /api/stacks': { body: [] },
    'GET /api/operations?state=running': { body: [] },
    'GET /api/me': { body: { username: 'admin' } },
    'GET /api/setup/status': { body: { needsSetup: false } },
    'GET /api/host': { body: {} },
  });
  h.stream = installFakeStream({
    '/api/events': [],
    '/api/stacks/web/logs': [],
    '/api/stacks/web/metrics/live': [],
  });
});
afterEach(async () => {
  cleanup();
  vi.unstubAllGlobals();
  await removeHarness(h);
});

const user = () => userEvent.setup();
const here = () => screen.getByTestId('location').textContent;
const two = () => stackDetail({ services: [appService(), dbService()] });

/** Like mountDetail, with the viewed server's features chosen by the test. */
function mount(path: string, features?: string[]) {
  function Probe() {
    const location = useLocation();
    const navigate = useNavigate();
    goTo = (href) => act(() => void navigate(href));
    return <output data-testid="location">{location.pathname + location.search}</output>;
  }
  const scope = features ? { ...localScope, features } : localScope;
  return render(
    <MemoryRouter initialEntries={[path]}>
      <ServerContext.Provider value={scope}>
        <Suspense fallback={<p>loading page</p>}>
          <Routes>
            {ROUTES.map(({ path: pattern, screen: Screen }) => (
              <Route key={pattern} path={pattern} element={pattern === '/' ? <p>home</p> : <Screen />} />
            ))}
          </Routes>
        </Suspense>
      </ServerContext.Provider>
      <Probe />
    </MemoryRouter>,
  );
}

function stubMobile(matches: boolean) {
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: query === '(max-width: 759px)' ? matches : false,
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
}

describe('header actions', () => {
  it('the header draws one cell per service and counts the failing one', async () => {
    h.stackRes = {
      body: stackDetail({
        status: 'partial',
        services: [appService(), dbService({ containers: [{ id: 'd', name: 'web-db-1', service: 'db', state: 'exited', status: 'Exited (1) 1 minute ago', health: 'none' }] })],
      }),
    };
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    expect(screen.getByRole('img', { name: '1 of 2 services healthy, 1 failing' })).toBeTruthy();
  });

  it('Remove stack in the More menu asks, and with volumes asks for the stack name', async () => {
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await user().click(screen.getByRole('button', { name: 'More actions' }));
    await user().click(screen.getByRole('button', { name: 'Remove stack' }));
    const dialog = await screen.findByRole('alertdialog');
    await user().click(within(dialog).getByRole('checkbox', { name: /also remove volumes/i }));
    expect(within(dialog).getByRole('button', { name: 'Remove' }).getAttribute('aria-disabled')).toBe('true');
  });

  it('View compose file in the More menu selects the compose item', async () => {
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await user().click(screen.getByRole('button', { name: 'More actions' }));
    await user().click(screen.getByRole('button', { name: 'View compose file' }));
    await waitFor(() => expect(here()).toBe('/stacks/web?tab=compose'));
  });
});

describe('logs on phones and expanded', () => {
  beforeEach(() => {
    h.stackRes = { body: two() };
  });

  it('mobile shows service chips and a Logs tab, not the dock', async () => {
    stubMobile(true);
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    expect(screen.getByRole('navigation', { name: 'Service chips' })).toBeTruthy();
    const tabs = within(screen.getByRole('tablist', { name: 'Service sections' }));
    expect(tabs.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Overview', 'Logs', 'Metrics', 'Config', 'Terminal']);
    expect(screen.queryByRole('region', { name: 'Logs' })).toBeNull();
    await user().click(tabs.getByRole('tab', { name: 'Logs' }));
    await waitFor(() => expect(here()).toBe('/stacks/web?service=app&tab=logs'));
    const panel = screen.getByRole('tabpanel');
    expect(within(panel).getByRole('region', { name: 'Logs' })).toBeTruthy();
  });

  it('mobile ?tab=logs selects the Logs tab', async () => {
    stubMobile(true);
    mount('/stacks/web?service=db&tab=logs');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await waitFor(() => expect(screen.getByRole('tab', { name: 'Logs' }).getAttribute('aria-selected')).toBe('true'));
    expect(within(screen.getByRole('tabpanel')).getByRole('region', { name: 'Logs' })).toBeTruthy();
  });

  it('wide screens keep the dock and no Logs tab', async () => {
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    expect(screen.queryByRole('tab', { name: 'Logs' })).toBeNull();
    expect(screen.getByRole('region', { name: 'Logs' })).toBeTruthy();
  });

  it('expand fills the pane with logs and collapse brings the service back', async () => {
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    expect(screen.getByRole('tablist', { name: 'Service sections' })).toBeTruthy();
    await user().click(screen.getByRole('button', { name: 'Expand logs' }));
    expect(screen.queryByRole('tablist', { name: 'Service sections' })).toBeNull();
    expect(screen.getByRole('region', { name: 'Logs' }).getAttribute('data-fill')).toBe('true');
    await user().click(screen.getByRole('button', { name: 'Collapse logs' }));
    expect(screen.getByRole('tablist', { name: 'Service sections' })).toBeTruthy();
    expect(screen.getByRole('region', { name: 'Logs' }).getAttribute('data-fill')).toBeNull();
  });

  it('expanded logs end when another service is selected', async () => {
    mount('/stacks/web');
    await screen.findByRole('heading', { level: 1, name: 'web' });
    await user().click(screen.getByRole('button', { name: 'Expand logs' }));
    await user().click(within(screen.getByRole('navigation', { name: 'Services' })).getByRole('button', { name: /^db\b/ }));
    expect(await screen.findByRole('tablist', { name: 'Service sections' })).toBeTruthy();
  });
});

describe('Restore .bak on a parse error', () => {
  beforeEach(() => {
    h.stackRes = { body: stackDetail({ status: 'unavailable', parseError: 'yaml: line 3: mapping values are not allowed', services: [] }) };
  });
  const restoreButton = () => screen.queryByRole('button', { name: 'Restore .bak' });

  it('Restore .bak appears only with hasBackup and the composeRestore feature', async () => {
    compose = { ...compose, hasBackup: true };
    mount('/stacks/web');
    expect(await screen.findByRole('button', { name: 'Restore .bak' })).toBeTruthy();
    cleanup();

    compose = { ...compose, hasBackup: false };
    mount('/stacks/web');
    await screen.findByText('Compose file cannot be parsed');
    await waitFor(() => expect(h.calls('GET', '/api/stacks/web/compose')).toBeGreaterThan(1));
    expect(restoreButton()).toBeNull();
    cleanup();

    compose = { ...compose, hasBackup: true };
    const before = h.calls('GET', '/api/stacks/web/compose');
    mount('/stacks/web', ['managed-dockerfile']);
    await screen.findByText('Compose file cannot be parsed');
    await new Promise((r) => setTimeout(r, 50));
    expect(restoreButton()).toBeNull();
    expect(h.calls('GET', '/api/stacks/web/compose')).toBe(before + 1); // the editor's own read; no backup check
  });

  it('is not offered when the compose file is missing', async () => {
    compose = { ...compose, hasBackup: true };
    h.stackRes = { body: stackDetail({ status: 'unavailable', parseError: 'open compose.yaml: no such file or directory', services: [] }) };
    mount('/stacks/web');
    await screen.findByText('Compose file is missing');
    await new Promise((r) => setTimeout(r, 50));
    expect(restoreButton()).toBeNull();
  });

  it('confirms, restores against the loaded hash, and reloads the stack', async () => {
    compose = { ...compose, hasBackup: true };
    mount('/stacks/web');
    await user().click(await screen.findByRole('button', { name: 'Restore .bak' }));
    const dialog = await screen.findByRole('alertdialog', { name: 'Restore compose.yaml.bak?' });
    expect(dialog.textContent).toContain('compose.yaml.broken');
    h.stackRes = { body: stackDetail() };
    await user().click(within(dialog).getByRole('button', { name: 'Restore file' }));
    await waitFor(() => expect(h.fake.calls.find((c) => c.path === '/api/stacks/web/compose/restore-backup')?.body).toEqual({ hash: 'c1' }));
    await waitFor(() => expect(screen.queryByText('Compose file cannot be parsed')).toBeNull());
    expect(get(toasts).some((t) => t.kind === 'success' && /restored/i.test(t.message))).toBe(true);
    expect(screen.queryByRole('alertdialog')).toBeNull();
  });

  it('a stale file keeps the dialog open and says to reload', async () => {
    compose = { ...compose, hasBackup: true };
    restoreRes = { status: 409, body: { error: 'compose file changed' } };
    mount('/stacks/web');
    await user().click(await screen.findByRole('button', { name: 'Restore .bak' }));
    const dialog = await screen.findByRole('alertdialog', { name: 'Restore compose.yaml.bak?' });
    await user().click(within(dialog).getByRole('button', { name: 'Restore file' }));
    expect((await within(dialog).findByRole('alert')).textContent).toMatch(/changed since it was loaded/i);
    expect(screen.getByText('Compose file cannot be parsed')).toBeTruthy();
  });
});

describe('edit=1 in the service URL', () => {
  beforeEach(() => {
    h.stackRes = { body: two() };
  });

  it("opens that service's editor and drops the parameter from the URL", async () => {
    mount('/stacks/web?service=db&edit=1');
    expect(await screen.findByLabelText('Image')).toBeTruthy();
    expect(screen.getByRole('heading', { level: 2, name: 'db' })).toBeTruthy();
    expect(screen.getByRole('button', { name: 'Close editor' })).toBeTruthy();
    expect(screen.queryByRole('tablist', { name: 'Service sections' })).toBeNull();
    await waitFor(() => expect(here()).toBe('/stacks/web?service=db'));
  });

  it('does nothing under a parse error, which has no services to edit', async () => {
    h.stackRes = { body: stackDetail({ status: 'unavailable', parseError: 'bad yaml', services: [] }) };
    mount('/stacks/web?service=db&edit=1');
    await screen.findByText('Compose file cannot be parsed');
    expect(screen.queryByLabelText('Image')).toBeNull();
  });

  it('goes through the unsaved-changes guard when the page holds an unsaved draft', async () => {
    mount('/stacks/web?service=app');
    await user().click(within(await screen.findByRole('navigation', { name: 'Services' })).getByRole('button', { name: 'Add service' }));
    await user().type(await screen.findByLabelText('Service name'), 'cache');
    await waitFor(() => expect(get(unsavedChanges)).toBe(true));

    goTo('/stacks/web?service=app&edit=1');
    const dialog = await screen.findByRole('alertdialog', { name: 'Discard unsaved changes?' });
    await user().click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('button', { name: 'Close editor' })).toBeNull();
    expect(screen.getByLabelText('Service name')).toBeTruthy(); // the draft survives

    goTo('/stacks/web?service=app&edit=1');
    await user().click(within(await screen.findByRole('alertdialog', { name: 'Discard unsaved changes?' })).getByRole('button', { name: 'Discard changes' }));
    expect(await screen.findByRole('button', { name: 'Close editor' })).toBeTruthy();
    expect(screen.queryByLabelText('Service name')).toBeNull();
  }, 15_000);
});
