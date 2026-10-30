import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ServiceDetail } from '$lib/api/types';
import { changes, startLive } from '$lib/stores/live';
import { get } from '$lib/store';
import { appService, installHarness, mountDetail, removeHarness, stackDetail, type Harness } from './detailTestKit';
import type { ServiceTab } from '$lib/stacks/detailLocation';
import { ServicePane } from './ServicePane';

// xterm needs a real canvas; the mock just reports the props the pane passes.
vi.mock('../containers/TerminalTab', () => ({
  default: (props: { containerId: string; containerName: string; running: boolean; onStart: () => void }) => (
    <div data-testid="terminal" data-id={props.containerId} data-running={String(props.running)}>
      {props.containerName}
      <button onClick={props.onStart}>start-from-terminal</button>
    </div>
  ),
}));

const user = () => userEvent.setup();
const flatLine = 'M0,17L60,17';

describe('service pane', () => {
  const renderPane = (service: ServiceDetail, over: Partial<Parameters<typeof ServicePane>[0]> = {}) =>
    render(
      <MemoryRouter>
        <ServicePane service={service} tab="overview" onTab={() => {}} onLogs={() => {}} latestCpu={undefined} cpuHour={undefined} {...over} />
      </MemoryRouter>,
    );
  const sparklinePath = () => document.querySelector('svg path')!.getAttribute('d');

  afterEach(() => cleanup());

  it('renders the header with the state and three tabs, Overview selected', () => {
    renderPane(appService());
    expect(screen.getByRole('heading', { level: 2, name: 'app' })).toBeTruthy();
    expect(screen.getByText('Up 2 minutes', { selector: 'header *' })).toBeTruthy();
    const tabs = within(screen.getByRole('tablist', { name: 'Service sections' }));
    expect(tabs.getByRole('tab', { name: 'Overview' }).getAttribute('aria-selected')).toBe('true');
    expect(tabs.getByRole('tab', { name: 'Metrics' }).getAttribute('aria-selected')).toBe('false');
    expect(tabs.getByRole('tab', { name: 'Config' }).getAttribute('aria-selected')).toBe('false');
  });

  it('Overview shows ports and volumes as chips, limits as cpus and memory, and dashes when empty', () => {
    renderPane(appService({ config: { image: 'nginx:1', ports: ['80:80'], limits: { cpus: '0.5', memory: '256m' }, restart: 'always' } }));
    expect(screen.getByText('nginx:1')).toBeTruthy();
    expect(screen.getByText(':80 → 80')).toBeTruthy();
    expect(screen.getByText('0.5 cpus · 256m')).toBeTruthy();
    expect(screen.getByText('always')).toBeTruthy();
    const volumes = screen.getByText('Volumes').nextElementSibling!;
    expect(volumes.textContent).toBe('—');
  });

  const library = (over: Partial<NonNullable<ServiceDetail['library']>> = {}) => ({
    ref: 'stackstead.invalid/0123456789ab/web:1',
    owner: '0123456789ab',
    ownerName: 'home',
    image: 'web',
    tag: '1',
    latestTag: '1',
    arch: 'amd64',
    newer: false,
    known: true,
    ...over,
  });

  it('shows a library image as name:tag (owner) and flags a newer version', () => {
    renderPane(appService({ config: { image: 'stackstead.invalid/0123456789ab/web:1', limits: {} }, library: library({ latestTag: '2', newer: true }) }));
    expect(screen.getByText('web:1 (home)')).toBeTruthy();
    expect(screen.getByText('new version available: 2')).toBeTruthy();
  });

  it('shows no badge when the library image is current', () => {
    renderPane(appService({ config: { image: 'stackstead.invalid/0123456789ab/web:1', limits: {} }, library: library() }));
    expect(screen.getByText('web:1 (home)')).toBeTruthy();
    expect(screen.queryByText(/new version available/)).toBeNull();
  });

  it('shows a build context when there is no image', () => {
    renderPane(appService({ config: { build: { context: './api' }, limits: {} } }));
    expect(screen.getByText('build: ./api')).toBeTruthy();
  });

  it('links the container name to its container page', () => {
    renderPane(appService());
    expect(screen.getByRole('link', { name: 'web-app-1' }).getAttribute('href')).toBe('/containers/web-app-1');
  });

  it('links each running replica to its terminal and skips stopped ones', () => {
    renderPane(
      appService({
        containers: [
          { id: 'a', name: 'web-app-1', service: 'app', state: 'running', status: 'Up', health: 'none' },
          { id: 'b', name: 'web-app-2', service: 'app', state: 'exited', status: 'Exited (0)', health: 'none' },
        ],
      }),
    );
    expect(screen.getByRole('link', { name: 'Open terminal for web-app-1' }).getAttribute('href')).toBe('/containers/web-app-1?tab=terminal');
    expect(screen.queryByRole('link', { name: 'Open terminal for web-app-2' })).toBeNull();
  });

  it('the container card shows the name on one line and the status alone on the next', () => {
    renderPane(appService());
    const item = screen.getByRole('link', { name: 'web-app-1' }).closest('li')!;
    const lines = Array.from(item.querySelectorAll(':scope > div')).map((line) => line.textContent);
    expect(lines).toEqual(['web-app-1', 'Up 2 minutes']);
  });

  describe('Terminal tab', () => {
    const replicas = () =>
      appService({
        containers: [
          { id: 'a', name: 'web-app-1', service: 'app', state: 'exited', status: 'Exited (0)', health: 'none' },
          { id: 'b', name: 'web-app-2', service: 'app', state: 'running', status: 'Up', health: 'none' },
          { id: 'c', name: 'web-app-3', service: 'app', state: 'running', status: 'Up', health: 'none' },
        ],
      });

    it('is listed next to Config and selecting it calls onTab', async () => {
      const picked: string[] = [];
      renderPane(appService(), { onTab: (tab) => picked.push(tab) });
      await user().click(within(screen.getByRole('tablist', { name: 'Service sections' })).getByRole('tab', { name: 'Terminal' }));
      expect(picked).toEqual(['terminal']);
    });

    it('opens the terminal of the only container', async () => {
      renderPane(appService(), { tab: 'terminal' });
      const terminal = await screen.findByTestId('terminal');
      expect(terminal.getAttribute('data-id')).toBe('abc');
      expect(terminal.getAttribute('data-running')).toBe('true');
      expect(screen.queryByRole('combobox')).toBeNull();
    });

    it('with replicas defaults to the first running container and switching remounts with the other one', async () => {
      renderPane(replicas(), { tab: 'terminal' });
      expect((await screen.findByTestId('terminal')).getAttribute('data-id')).toBe('b');
      const select = screen.getByRole('combobox', { name: 'Container' }) as HTMLSelectElement;
      expect(Array.from(select.options).map((o) => o.text)).toEqual(['web-app-1', 'web-app-2', 'web-app-3']);
      expect(select.value).toBe('b');
      await user().selectOptions(select, 'c');
      expect(screen.getByTestId('terminal').getAttribute('data-id')).toBe('c');
      await user().selectOptions(select, 'a');
      expect(screen.getByTestId('terminal').getAttribute('data-running')).toBe('false');
    });

    it('says so when the service has no containers', () => {
      renderPane(appService({ containers: [] }), { tab: 'terminal' });
      expect(screen.getByText('No container for this service. Start the stack to open a terminal.')).toBeTruthy();
      expect(screen.queryByTestId('terminal')).toBeNull();
    });

    it('onStart from the terminal calls the pane onStart', async () => {
      let started = 0;
      renderPane(appService(), { tab: 'terminal', onStart: () => started++ });
      await user().click(await screen.findByRole('button', { name: 'start-from-terminal' }));
      expect(started).toBe(1);
    });

    it('the Overview Terminal link switches to the tab for that container instead of navigating', async () => {
      const picked: string[] = [];
      function Harness() {
        const [tab, setTab] = useState<ServiceTab>('overview');
        return (
          <MemoryRouter>
            <ServicePane service={replicas()} tab={tab} onTab={(t) => { picked.push(t); setTab(t); }} onLogs={() => {}} latestCpu={undefined} cpuHour={undefined} />
          </MemoryRouter>
        );
      }
      render(<Harness />);
      await user().click(screen.getByRole('link', { name: 'Open terminal for web-app-3' }));
      expect(picked).toEqual(['terminal']);
      expect((await screen.findByTestId('terminal')).getAttribute('data-id')).toBe('c');
    });
  });

  it('says so when the service has no containers', () => {
    renderPane(appService({ containers: [] }));
    expect(screen.getByText('No containers.')).toBeTruthy();
  });

  it('shows the read-only note', () => {
    renderPane(appService({ readOnly: ['environment', 'healthcheck'] }));
    expect(screen.getByText(/Read-only in the form: environment, healthcheck/)).toBeTruthy();
  });

  it('CPU · 1h shows the live value and a sparkline from the hour history', () => {
    const view = renderPane(appService(), { latestCpu: 12.5, cpuHour: [1, null, 4, 2] });
    const card = within(screen.getByText('CPU · 1h').parentElement!.parentElement!);
    expect(card.getByText('12.5%')).toBeTruthy();
    expect(sparklinePath()).not.toBe(flatLine);
    expect(sparklinePath()).toMatch(/^M/);
    view.unmount();
    renderPane(appService());
    expect(sparklinePath()).toBe(flatLine);
  });

  it('Logs calls onLogs', async () => {
    let calls = 0;
    renderPane(appService(), { onLogs: () => calls++ });
    await user().click(screen.getByRole('button', { name: 'Logs for app' }));
    expect(calls).toBe(1);
  });

  it('tab clicks report the tab and other tabs render through children', async () => {
    const picked: string[] = [];
    renderPane(appService(), { tab: 'config', onTab: (t) => picked.push(t), children: (tab) => <p>content for {tab}</p> });
    expect(screen.getByText('content for config')).toBeTruthy();
    expect(screen.queryByText('Image')).toBeNull();
    await user().click(screen.getByRole('tab', { name: 'Metrics' }));
    expect(picked).toEqual(['metrics']);
  });
});

describe('stack detail service pane and compose view', () => {
  let h: Harness;
  beforeEach(() => {
    h = installHarness();
  });
  afterEach(async () => {
    cleanup();
    await removeHarness(h);
  });

  const here = () => screen.getByTestId('location').textContent;
  const cpuCalls = () => h.fake.calls.filter((c) => c.method === 'GET' && c.path.startsWith('/api/stacks/web/metrics?'));

  it('?tab=config and ?tab=metrics select the tab', async () => {
    mountDetail(h, '/stacks/web?tab=config');
    expect((await screen.findByRole('tab', { name: 'Config' })).getAttribute('aria-selected')).toBe('true');
    cleanup();
    mountDetail(h, '/stacks/web?tab=metrics');
    expect((await screen.findByRole('tab', { name: 'Metrics' })).getAttribute('aria-selected')).toBe('true');
  });

  it('tab clicks navigate', async () => {
    mountDetail(h, '/stacks/web');
    await user().click(await screen.findByRole('tab', { name: 'Config' }));
    await waitFor(() => expect(here()).toBe('/stacks/web?service=app&tab=config'));
    await user().click(screen.getByRole('tab', { name: 'Overview' }));
    await waitFor(() => expect(here()).toBe('/stacks/web?service=app'));
  });

  it('the Overview tab fetches the service\'s hour of cpu once with step 2m', async () => {
    mountDetail(h, '/stacks/web');
    await screen.findByRole('heading', { level: 2, name: 'app' });
    await waitFor(() => expect(cpuCalls()).toHaveLength(1));
    expect(cpuCalls()[0]!.path).toMatch(/step=2m/);
    expect(cpuCalls()[0]!.path).toMatch(/service=app/);
    h.goTo('/stacks/web?service=db');
    await screen.findByRole('heading', { level: 2, name: 'db' });
    await waitFor(() => expect(cpuCalls()).toHaveLength(2));
    h.goTo('/stacks/web');
    await screen.findByRole('heading', { level: 2, name: 'app' });
    await new Promise((r) => setTimeout(r, 30));
    expect(cpuCalls()).toHaveLength(2);
  });

  it('asks for the hour of cpu again after a refetch', async () => {
    mountDetail(h, '/stacks/web');
    await screen.findByRole('heading', { level: 2, name: 'app' });
    await waitFor(() => expect(cpuCalls()).toHaveLength(1));
    // Live starts once the page is subscribed, so its resync is a refetch and not a value replayed on subscribe.
    startLive({ fetchStatus: () => fetch('/api/status').then((r) => r.json()) });
    const stackGets = () => h.calls('GET', '/api/stacks/web');
    await waitFor(() => expect(stackGets()).toBe(2)); // initial + resync
    // The first load and the resync's refetch each ask once; only then is the count stable.
    await waitFor(() => expect(cpuCalls()).toHaveLength(2));
    const before = cpuCalls().length;
    h.stream.push('/api/events', 'event: change\ndata: {"project":"web","seq":9}\n\n');
    await waitFor(() => expect(stackGets()).toBe(3));
    await waitFor(() => expect(cpuCalls()).toHaveLength(before + 1));
    expect(cpuCalls().at(-1)!.path).toMatch(/service=app/);
  });

  it('?tab=compose shows the file with its name and path', async () => {
    mountDetail(h, '/stacks/web?tab=compose');
    expect(await screen.findByRole('heading', { level: 2, name: 'compose.yaml' })).toBeTruthy();
    expect(within(screen.getByRole('heading', { level: 2, name: 'compose.yaml' }).parentElement!).getByText('/srv/stacks/web/compose.yaml')).toBeTruthy();
    await waitFor(() => expect(document.querySelector('pre')?.textContent).toBe('services: {}\n'));
    expect(h.calls('GET', '/api/stacks/web/compose')).toBe(1);
  });

  it('a compose load failure shows Retry', async () => {
    h.composeRes = { status: 500, body: { error: 'disk on fire' } };
    mountDetail(h, '/stacks/web?tab=compose');
    expect(await screen.findByText('Could not load the compose file')).toBeTruthy();
    h.composeRes = { body: { content: 'services: {}\n', hash: 'c1' } };
    await user().click(screen.getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(document.querySelector('pre')?.textContent).toBe('services: {}\n'));
    expect(screen.queryByText('Could not load the compose file')).toBeNull();
  });

  it('under a parse error the compose view says the file cannot be edited with forms', async () => {
    h.stackRes = { body: stackDetail({ status: 'unavailable', parseError: 'yaml: line 3: bad', services: [] }) };
    mountDetail(h, '/stacks/web');
    expect(await screen.findByText('This file cannot be edited with forms')).toBeTruthy();
    expect(await screen.findByRole('heading', { level: 2, name: 'compose.yaml' })).toBeTruthy();
  });

  it('?tab=services (legacy) shows the first service overview', async () => {
    mountDetail(h, '/stacks/web?tab=services');
    expect(await screen.findByRole('heading', { level: 2, name: 'app' })).toBeTruthy();
    expect(screen.getByRole('tab', { name: 'Overview' }).getAttribute('aria-selected')).toBe('true');
  });

  it('a service in the URL selects its pane', async () => {
    mountDetail(h, '/stacks/web?service=db');
    expect(await screen.findByRole('heading', { level: 2, name: 'db' })).toBeTruthy();
    expect(screen.getByText('postgres:16')).toBeTruthy();
  });
});
