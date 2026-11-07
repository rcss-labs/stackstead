import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '$lib/api/client';
import type { HostSnapshot, Operation, PeerView, ServerSelf, Status } from '$lib/api/types';
import { startPeers } from '$lib/servers/peers';
import { installFakeFetch, installFakeStream } from '../../vitest-setup';
import RemoteServer from '../servers/RemoteServer';
import { setTheme } from '$lib/stores/appearance';
import { startHost } from '$lib/stores/host';
import { startLive, stopLive } from '$lib/stores/live';
import { HostBlock } from './HostBlock';
import { MobileTopBar } from './MobileTopBar';
import { Shell } from './Shell';
import { Sidebar } from './Sidebar';
import { TabBar } from './TabBar';
import { TopBar } from './TopBar';
import { pageBack, pageTitle } from './nav';

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// useStore releases its store subscription on the next task after unmount; wait so every test starts from a clean hub.
afterEach(async () => {
  cleanup();
  vi.unstubAllGlobals();
  setTheme('system');
  await sleep(5);
});

function Where() {
  return <span data-testid="where">{useLocation().pathname}</span>;
}

function renderIn(ui: React.ReactElement, at = '/') {
  return render(
    <MemoryRouter initialEntries={[at]}>
      {ui}
      <Where />
    </MemoryRouter>,
  );
}

const sidebarProps = { onLogout: () => {}, stackCount: 3, attention: false, onOpenPalette: () => {} };
const current = (name: string | RegExp) => screen.getByRole('link', { name }).getAttribute('aria-current');

describe('Sidebar', () => {
  it('links to the main pages and marks the current one', () => {
    renderIn(<Sidebar {...sidebarProps} username="ana" current="/settings" />);
    expect(screen.getByRole('navigation', { name: 'Main' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Overview' }).getAttribute('href')).toBe('/');
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/stacks/new');
    expect(screen.getByRole('link', { name: 'Import' }).getAttribute('href')).toBe('/stacks/import');
    expect(current('Settings')).toBe('page');
    expect(screen.getByRole('link', { name: 'Settings' }).getAttribute('title')).toBe('Settings');
    expect(current('Overview')).toBeNull();
  });

  it('keeps Overview current on a stack detail path but not on create or import', () => {
    const { rerender } = renderIn(<Sidebar {...sidebarProps} current="/stacks/web" />);
    const again = (path: string) =>
      rerender(
        <MemoryRouter>
          <Sidebar {...sidebarProps} current={path} />
        </MemoryRouter>,
      );
    expect(current('Overview')).toBe('page');
    again('/stacks/new');
    expect(current('Overview')).toBeNull();
    expect(current('New stack')).toBe('page');
    again('/stacks/import');
    expect(current('Overview')).toBeNull();
  });

  it('has a Containers item after Overview, current on the list and on a container page only', () => {
    const { rerender } = renderIn(<Sidebar {...sidebarProps} current="/containers" />);
    const again = (path: string) =>
      rerender(
        <MemoryRouter>
          <Sidebar {...sidebarProps} current={path} />
        </MemoryRouter>,
      );
    const names = within(screen.getByRole('navigation', { name: 'Main' }))
      .getAllByRole('link')
      .map((a) => a.getAttribute('aria-label'));
    expect(names.indexOf('Containers')).toBe(names.indexOf('Overview') + 1);
    const item = screen.getByRole('link', { name: 'Containers' });
    expect(item.getAttribute('href')).toBe('/containers');
    expect(item.getAttribute('title')).toBe('Containers');
    expect(current('Containers')).toBe('page');
    expect(current('Overview')).toBeNull();
    again('/containers/redis');
    expect(current('Containers')).toBe('page');
    again('/');
    expect(current('Containers')).toBeNull();
  });

  it('places Networks after Images and marks it current on the list and a selected network', () => {
    const { rerender } = renderIn(<Sidebar {...sidebarProps} current="/networks" />);
    const names = screen.getAllByRole('link').map((a) => a.getAttribute('aria-label'));
    expect(names.indexOf('Networks')).toBe(names.indexOf('Images') + 1);
    const item = screen.getByRole('link', { name: 'Networks' });
    expect(item.getAttribute('href')).toBe('/networks');
    expect(item.getAttribute('title')).toBe('Networks');
    expect(current('Networks')).toBe('page');
    expect(item.querySelector('path')?.getAttribute('d')).toBeTruthy();
    rerender(
      <MemoryRouter>
        <Sidebar {...sidebarProps} current="/networks/other" />
      </MemoryRouter>,
    );
    expect(current('Networks')).toBe('page');
    for (const path of ['/networksx', '/containers', '/']) {
      rerender(
        <MemoryRouter>
          <Sidebar {...sidebarProps} current={path} />
        </MemoryRouter>,
      );
      expect(current('Networks')).toBeNull();
    }
  });

  it('lists Images first under Resources and marks it current below its route', () => {
    renderIn(<Sidebar {...sidebarProps} current="/images/web" />);
    const labels = screen.getAllByRole('link').map((a) => a.getAttribute('aria-label'));
    expect(labels.indexOf('Images')).toBe(labels.indexOf('Activity') + 1);
    expect(screen.getByRole('link', { name: 'Images' }).getAttribute('href')).toBe('/images');
    expect(current('Images')).toBe('page');
    expect(current('Storage')).toBeNull();
  });

  it('groups the pages: Overview, Containers, Activity; Resources; Add', () => {
    renderIn(<Sidebar {...sidebarProps} current="/" />);
    const names = within(screen.getByRole('navigation', { name: 'Main' }))
      .getAllByRole('link')
      .map((a) => a.getAttribute('aria-label'));
    expect(names).toEqual(['Overview', 'Containers', 'Activity', 'Images', 'Networks', 'Storage', 'Backups', 'New stack', 'Import', 'Settings']);
    expect(screen.getByRole('link', { name: 'Activity' }).getAttribute('href')).toBe('/activity');
    expect(screen.getByRole('link', { name: 'Backups' }).getAttribute('href')).toBe('/backups');
    expect(screen.getByText('Resources')).toBeTruthy();
    expect(screen.getByText('Add')).toBeTruthy();
  });

  it('marks Storage current below its route after Networks', () => {
    renderIn(<Sidebar {...sidebarProps} current="/storage/images/abcdef123456" />);
    const labels = screen.getAllByRole('link').map((a) => a.getAttribute('aria-label'));
    expect(labels.indexOf('Storage')).toBe(labels.indexOf('Networks') + 1);
    expect(current('Storage')).toBe('page');
    expect(screen.getByRole('link', { name: 'Storage' }).querySelector('path')?.getAttribute('d')).toBeTruthy();
  });

  it('navigates client-side when a link is clicked', async () => {
    let reloaded = false;
    const onBefore = () => (reloaded = true);
    window.addEventListener('beforeunload', onBefore);
    renderIn(<Sidebar {...sidebarProps} current="/" />);
    await userEvent.setup().click(screen.getByRole('link', { name: 'Settings' }));
    expect(screen.getByTestId('where').textContent).toBe('/settings');
    expect(reloaded).toBe(false);
    window.removeEventListener('beforeunload', onBefore);
  });

  it('theme toggle label flips and calls setTheme', async () => {
    setTheme('dark');
    renderIn(<Sidebar {...sidebarProps} current="/" />);
    const user = userEvent.setup();
    await user.click(screen.getByRole('button', { name: 'Switch to light theme' }));
    expect(screen.getByRole('button', { name: 'Switch to dark theme' }).getAttribute('title')).toBe('Switch to dark theme');
    await user.click(screen.getByRole('button', { name: 'Switch to dark theme' }));
    expect(screen.getByRole('button', { name: 'Switch to light theme' })).toBeTruthy();
  });

  it('the avatar is named after the user so it never collides with the Settings Log out button', () => {
    renderIn(<Sidebar {...sidebarProps} username="ana" onLogout={vi.fn()} current="/" />);
    expect(screen.getByRole('button', { name: 'Log out ana' }).getAttribute('title')).toBe('ana · Log out');
  });

  it('avatar shows initials and logs out through onLogout', async () => {
    const onLogout = vi.fn();
    renderIn(<Sidebar {...sidebarProps} username="ana" onLogout={onLogout} current="/" />);
    const avatar = screen.getByRole('button', { name: 'Log out ana' });
    expect(avatar.textContent?.trim()).toBe('AN');
    expect(avatar.getAttribute('title')).toBe('ana · Log out');
    await userEvent.setup().click(avatar);
    expect(onLogout).toHaveBeenCalledTimes(1);
  });

  it('shows the attention LED on Overview only when attention is true', () => {
    const { rerender } = renderIn(<Sidebar {...sidebarProps} attention={false} current="/" />);
    const led = () => screen.getByRole('link', { name: /^Overview(,|$)/ }).querySelector('[data-tone="danger"]');
    expect(led()).toBeNull();
    rerender(
      <MemoryRouter>
        <Sidebar {...sidebarProps} attention current="/" />
      </MemoryRouter>,
    );
    expect(led()?.getAttribute('data-pulse')).toBe('true');
    expect(screen.getByRole('link', { name: 'Containers' }).querySelector('[data-tone="danger"]')).toBeNull();
  });

  it('announces the attention LED as part of the Overview link name', () => {
    renderIn(<Sidebar {...sidebarProps} attention current="/" />);
    expect(screen.getByRole('link', { name: /Overview.*needs attention/i })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Containers' })).toBeTruthy();
  });

  it('search button opens the palette', async () => {
    const onOpenPalette = vi.fn();
    renderIn(<Sidebar {...sidebarProps} onOpenPalette={onOpenPalette} current="/" />);
    await userEvent.setup().click(screen.getByRole('button', { name: 'Search or run a command' }));
    expect(onOpenPalette).toHaveBeenCalledTimes(1);
  });

  describe('versions line', () => {
    const base: Status = {
      docker: 'ok',
      dockerVersion: '27.1.1',
      compose: '2.29.2',
      events: 'connected',
      metricsDegraded: false,
      metricsIntervalSeconds: 10,
      metricsRetentionSeconds: 259200,
    };
    async function withStatus(s: Partial<Status>) {
      const status = { ...base, ...s };
      startLive({
        fetchStatus: async () => status,
        open: (o) => {
          queueMicrotask(() => o.onStatus?.('open'));
          return { status: 'open', close() {} };
        },
      });
      await sleep(0);
    }
    beforeEach(() => stopLive());
    afterEach(() => stopLive());

    it('shows Docker and compose, unavailable states, and is neutral before the first status', async () => {
      const neutral = renderIn(<Sidebar {...sidebarProps} current="/" />);
      expect(screen.getByText('Docker')).toBeTruthy();
      expect(screen.queryByText(/compose/)).toBeNull();
      neutral.unmount();

      await withStatus({});
      let view = renderIn(<Sidebar {...sidebarProps} current="/" />);
      expect(screen.getByText('Docker 27.1.1 · compose 2.29.2')).toBeTruthy();
      view.unmount();
      stopLive();

      await withStatus({ dockerVersion: '' });
      view = renderIn(<Sidebar {...sidebarProps} current="/" />);
      expect(screen.getByText('Docker · compose 2.29.2')).toBeTruthy();
      view.unmount();
      stopLive();

      await withStatus({ docker: 'unavailable', dockerVersion: '', compose: 'unavailable' });
      view = renderIn(<Sidebar {...sidebarProps} current="/" />);
      expect(screen.getByText('Docker unavailable · compose unavailable')).toBeTruthy();
    });
  });
});

describe('HostBlock', () => {
  const GiB = 1024 ** 3;
  const snapshot = (over: Partial<HostSnapshot> = {}): HostSnapshot =>
    ({
      hostname: 'nas-01',
      os: 'linux',
      source: 'proc',
      sampledAt: '2026-10-19T10:00:00Z',
      cpu: { percent: 23.4, cores: 4 },
      memory: { used: 9.6 * GiB, total: 16 * GiB, swapUsed: 0, swapTotal: 0 },
      disk: { path: '/', free: 22, total: 100 },
      uptime: { seconds: 3 * 86400 + 3600, load1: 0, load5: 0, load15: 0 },
      ...over,
    }) as HostSnapshot;
  let release: (() => void) | undefined;
  afterEach(() => {
    release?.();
    release = undefined;
  });
  const mountWith = (snap: HostSnapshot) => {
    release = startHost({ host: async () => snap });
    return render(<HostBlock />);
  };
  const bar = (container: HTMLElement, name: string) => container.querySelector(`[data-bar="${name}"]`);

  it('renders a placeholder before the first snapshot', () => {
    render(<HostBlock />);
    expect(screen.getByText('…')).toBeTruthy();
  });

  it('renders hostname, uptime and the three bars from the host store', async () => {
    const { container } = mountWith(snapshot());
    expect(await screen.findByText('nas-01')).toBeTruthy();
    expect(screen.getByText('3d up')).toBeTruthy();
    expect(screen.getByText('23.4%')).toBeTruthy();
    expect(screen.getByText('9.6G')).toBeTruthy();
    expect(screen.getByText('78%')).toBeTruthy();
    expect(bar(container, 'cpu')?.getAttribute('data-tone')).toBe('accent');
  });

  it('null sections render an em dash', async () => {
    const { container } = mountWith(snapshot({ cpu: null, memory: null, disk: null, uptime: null }));
    await screen.findByText('nas-01');
    expect(screen.getAllByText('—')).toHaveLength(3);
    expect(bar(container, 'cpu')?.getAttribute('data-empty')).toBe('true');
  });

  it('disk at 78% is warning, 92% danger', async () => {
    const tones: Record<number, string> = { 74: 'accent', 78: 'warning', 92: 'danger' };
    for (const [pct, tone] of Object.entries(tones)) {
      release = startHost({ host: async () => snapshot({ disk: { path: '/', free: 100 - Number(pct), total: 100 } }) });
      const view = render(<HostBlock />);
      await waitFor(() => expect(within(view.container).getByText(`${pct}%`)).toBeTruthy());
      expect(bar(view.container, 'disk')?.getAttribute('data-tone')).toBe(tone);
      view.unmount();
      release();
      release = undefined;
    }
  });
});

describe('TopBar', () => {
  const op = (id: string, action: string, stack: string): Operation => ({
    id,
    stack,
    action,
    state: 'running',
    startedAt: '2026-10-19T10:00:00Z',
  });
  beforeEach(() => stopLive());
  afterEach(() => stopLive());

  it('renders the breadcrumb with the last crumb current', () => {
    renderIn(<TopBar path="/stacks/web" runningOps={[]} />);
    const nav = screen.getByRole('navigation', { name: 'Breadcrumb' });
    expect(nav.querySelector('a')?.getAttribute('href')).toBe('/');
    const last = screen.getByText('web');
    expect(last.getAttribute('aria-current')).toBe('page');
    expect(last.hasAttribute('data-mono')).toBe(true);
  });

  it('shows the running pill only when there are running operations', () => {
    const { rerender } = renderIn(<TopBar path="/" runningOps={[]} />);
    const again = (ops: Operation[]) =>
      rerender(
        <MemoryRouter>
          <TopBar path="/" runningOps={ops} />
        </MemoryRouter>,
      );
    expect(screen.queryByRole('status')).toBeNull();
    again([op('1', 'pull', 'immich'), op('2', 'up', 'web')]);
    expect(screen.getByRole('status').textContent?.trim()).toBe('2 running');
    again([op('1', 'pull', 'immich')]);
    expect(screen.getByRole('status').textContent?.trim()).toBe('pull · immich');
  });

  it('labels an image operation by its image', () => {
    renderIn(<TopBar path="/" runningOps={[op('1', 'build', 'image/0123456789ab/web')]} />);
    expect(screen.getByRole('status').textContent?.trim()).toBe('build · image web');
  });

  it('live pill reflects the connection', async () => {
    const connect = async (status: 'open' | 'reconnecting' | 'closed') => {
      stopLive();
      startLive({
        fetchStatus: async () => ({}) as Status,
        open: (o) => {
          queueMicrotask(() => o.onStatus?.(status));
          return { status, close() {} };
        },
      });
      await sleep(0);
    };
    await connect('open');
    let view = renderIn(<TopBar path="/" runningOps={[]} />);
    expect(screen.getByText('live').closest('[data-live]')?.getAttribute('data-live')).toBe('open');
    view.unmount();
    await connect('reconnecting');
    view = renderIn(<TopBar path="/" runningOps={[]} />);
    expect(screen.getByText('reconnecting').closest('[data-live]')?.getAttribute('data-live')).toBe('reconnecting');
    view.unmount();
    await connect('closed');
    renderIn(<TopBar path="/" runningOps={[]} />);
    expect(screen.queryByText('live')).toBeNull();
    expect(screen.queryByText('reconnecting')).toBeNull();
  });
});

describe('pageTitle', () => {
  it.each([
    ['/', 'Overview', false],
    ['/containers', 'Containers', false],
    ['/networks', 'Networks', false],
    ['/storage', 'Storage', false],
    ['/settings', 'Settings', false],
    ['/settings/backups', 'Backups', false],
    ['/activity', 'Activity', false],
    ['/ports', 'Ports', false],
    ['/more', 'More', false],
    ['/stacks/new', 'New stack', false],
    ['/stacks/import', 'Import', false],
    ['/stacks/web', 'web', true],
    ['/containers/redis', 'redis', true],
  ])('%s', (path, title, mono) => {
    expect(pageTitle(path)).toEqual({ title, mono });
  });
});

describe('Mobile', () => {
  // The media store keeps its last value between mounts, so desktop stubs an explicit non-match.
  const stubViewport = (matches: boolean) =>
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches,
      media: query,
      addEventListener() {},
      removeEventListener() {},
    }));
  const shellProps = { onLogout: () => {}, runningOps: [], attention: false, onOpenPalette: () => {} };
  const tabProps = { attention: false, onOpenPalette: () => {} };
  // Tabs are links, except Search which is a button.
  const tabLabels = () =>
    within(screen.getByRole('navigation', { name: 'Main' }))
      .getAllByRole('link')
      .concat(within(screen.getByRole('navigation', { name: 'Main' })).getAllByRole('button'))
      .sort((a, b) => (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING ? -1 : 1))
      .map((el) => el.getAttribute('aria-label') ?? '');

  it('under 760px the sidebar is replaced by the mobile top bar and tab bar with one Main landmark', async () => {
    stubViewport(true);
    renderIn(
      <Shell {...shellProps} username="ana">
        <main>
          <h1>Stacks</h1>
        </main>
      </Shell>,
    );
    // The phone chrome is a lazily loaded chunk.
    await screen.findByRole('navigation', { name: 'Main' });
    expect(screen.getAllByRole('navigation', { name: 'Main' })).toHaveLength(1);
    expect(screen.queryByRole('button', { name: /^Log out/ })).toBeNull();
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/stacks/new');
    expect(screen.getAllByRole('main')).toHaveLength(1);
    expect(tabLabels()).toEqual(['Home', 'Containers', 'Search', 'Activity', 'More']);
  });

  it('tab bar has exactly Home, Containers, Search, Activity, More', () => {
    renderIn(<TabBar {...tabProps} current="/" />);
    expect(tabLabels()).toEqual(['Home', 'Containers', 'Search', 'Activity', 'More']);
    expect(screen.getByRole('link', { name: 'Home' }).getAttribute('href')).toBe('/');
    expect(screen.getByRole('link', { name: 'Containers' }).getAttribute('href')).toBe('/containers');
    expect(screen.getByRole('link', { name: 'Activity' }).getAttribute('href')).toBe('/activity');
    expect(screen.getByRole('link', { name: 'More' }).getAttribute('href')).toBe('/more');
  });

  it('the Search tab opens the palette instead of navigating', async () => {
    const onOpenPalette = vi.fn();
    renderIn(<TabBar {...tabProps} onOpenPalette={onOpenPalette} current="/containers" />);
    await userEvent.setup().click(screen.getByRole('button', { name: 'Search' }));
    expect(onOpenPalette).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('where').textContent).toBe('/');
  });

  it('pages outside the other four tabs are under More', () => {
    const { rerender } = renderIn(<TabBar {...tabProps} current="/images/web" />);
    const at = (path: string) =>
      rerender(
        <MemoryRouter>
          <TabBar {...tabProps} current={path} />
        </MemoryRouter>,
      );
    expect(current('More')).toBe('page');
    for (const [path, tab] of [['/stacks/web', 'Home'], ['/stacks/new', 'Home'], ['/activity', 'Activity'], ['/containers/redis', 'Containers'], ['/settings/system', 'More'], ['/ports', 'More']] as const) {
      at(path);
      expect(current(tab)).toBe('page');
      expect(tabLabels().filter((l) => l !== 'Search' && l !== tab).every((l) => current(l) === null)).toBe(true);
    }
  });

  it('crossing 760px keeps the page mounted with its state', async () => {
    const user = userEvent.setup();
    let listener: ((e: { matches: boolean }) => void) | undefined;
    vi.stubGlobal('matchMedia', (query: string) => ({
      matches: false,
      media: query,
      addEventListener(_type: string, fn: (e: { matches: boolean }) => void) {
        listener = fn;
      },
      removeEventListener() {
        listener = undefined;
      },
    }));
    renderIn(
      <Shell {...shellProps} username="ana">
        <main>
          <input aria-label="Draft" />
        </main>
      </Shell>,
    );
    await user.type(screen.getByRole('textbox', { name: 'Draft' }), 'hello');
    const before = screen.getByRole('textbox', { name: 'Draft' }) as HTMLInputElement;

    act(() => listener?.({ matches: true }));
    const onMobile = screen.getByRole('textbox', { name: 'Draft' }) as HTMLInputElement;
    expect(onMobile).toBe(before);
    expect(onMobile.value).toBe('hello');
    expect(tabLabels()).toHaveLength(5);
    expect(screen.queryByRole('button', { name: /^Log out/ })).toBeNull();
    expect(screen.getAllByRole('main')).toHaveLength(1);

    act(() => listener?.({ matches: false }));
    const backOnDesktop = screen.getByRole('textbox', { name: 'Draft' }) as HTMLInputElement;
    expect(backOnDesktop).toBe(before);
    expect(backOnDesktop.value).toBe('hello');
    expect(screen.getByRole('button', { name: /^Log out/ })).toBeTruthy();
    expect(screen.getAllByRole('main')).toHaveLength(1);
  });

  it('on desktop the sidebar shows with one Main landmark and no tab bar', () => {
    stubViewport(false);
    renderIn(
      <Shell {...shellProps} username="ana">
        <main />
      </Shell>,
    );
    expect(screen.getAllByRole('navigation', { name: 'Main' })).toHaveLength(1);
    expect(screen.getByRole('button', { name: 'Log out ana' })).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'Settings' })?.closest('nav')).toBe(screen.getByRole('navigation', { name: 'Main' }));
  });

  it('tab bar marks the current page and shows the alert LED on Home', () => {
    renderIn(<TabBar {...tabProps} current="/containers/redis" attention />);
    expect(current('Containers')).toBe('page');
    expect(current(/^Home(,|$)/)).toBeNull();
    expect(screen.getByRole('link', { name: /^Home(,|$)/ }).querySelector('[data-tone="danger"]')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Containers' }).querySelector('[data-tone="danger"]')).toBeNull();
  });

  it('tab bar and mobile top bar announce the attention LED', () => {
    renderIn(
      <>
        <TabBar {...tabProps} current="/" attention />
        <MobileTopBar title="Overview" mono={false} attention />
      </>,
    );
    expect(screen.getByRole('link', { name: /Home.*needs attention/i })).toBeTruthy();
    expect(screen.getAllByRole('img', { name: 'Needs attention' })).toHaveLength(1);
  });

  it('mobile title is mono for an entity route', () => {
    const { rerender } = renderIn(<MobileTopBar title="redis" mono attention={false} />);
    expect(screen.queryByRole('heading')).toBeNull();
    expect(screen.getByText('redis').hasAttribute('data-mono')).toBe(true);
    rerender(
      <MemoryRouter>
        <MobileTopBar title="Networks" mono={false} attention />
      </MemoryRouter>,
    );
    expect(screen.getByText('Networks').hasAttribute('data-mono')).toBe(false);
  });

  it('a detail page gets a back link to its parent in place of the server switcher; top-level pages do not', () => {
    const { rerender } = renderIn(<MobileTopBar title="redis" mono attention={false} back="/containers" />);
    expect(screen.getByRole('link', { name: 'Back' }).getAttribute('href')).toBe('/containers');
    rerender(
      <MemoryRouter>
        <MobileTopBar title="Containers" mono={false} attention={false} />
      </MemoryRouter>,
    );
    expect(screen.queryByRole('link', { name: 'Back' })).toBeNull();
  });

  it('pageBack follows the breadcrumb parent, inside a connected server, and goes to More from a settings section', () => {
    expect(pageBack('/')).toBeUndefined();
    expect(pageBack('/containers')).toBeUndefined();
    expect(pageBack('/containers/redis')).toBe('/containers');
    expect(pageBack('/stacks/web')).toBe('/');
    expect(pageBack('/stacks/new')).toBe('/');
    expect(pageBack('/s/mini-02')).toBeUndefined();
    expect(pageBack('/s/mini-02/containers')).toBeUndefined();
    expect(pageBack('/s/mini-02/containers/redis')).toBe('/s/mini-02/containers');
    expect(pageBack('/s/mini-02/stacks/web')).toBe('/s/mini-02');
    expect(pageBack('/settings/system')).toBe('/more');
  });
});

const peer = (over: Partial<PeerView> = {}): PeerView => ({
  id: 'b1',
  name: 'mini-02',
  slug: 'mini-02',
  url: 'https://mini-02:8080',
  version: '1.5',
  status: 'reachable',
  stackCount: 4,
  lastSeenAt: null,
  lastError: '',
  compatible: true,
  plaintext: false,
  ...over,
});

describe('server navigation', () => {
  let stopPeers: (() => void) | undefined;
  afterEach(() => {
    stopPeers?.();
    stopPeers = undefined;
  });
  const withPeers = async (list: PeerView[], self: () => Promise<ServerSelf> = async () => ({ name: 'ratsrv01' }) as ServerSelf) => {
    stopPeers = startPeers({ list: async () => list, self });
    await sleep(0);
  };
  const props = { ...sidebarProps, stackCount: 3 };
  const switcher = () => screen.getByRole('button', { name: /Switch server/ });
  const openMenu = async (user: ReturnType<typeof userEvent.setup>) => {
    await user.click(switcher());
    return screen.getByRole('menu');
  };

  it('the switcher names this server and offers only itself and Connect a server without peers', async () => {
    const user = userEvent.setup();
    renderIn(<Sidebar {...props} current="/" />);
    expect(screen.queryByRole('link', { name: 'All stacks' })).toBeNull();
    expect(switcher().getAttribute('aria-expanded')).toBe('false');
    const menu = await openMenu(user);
    expect(switcher().getAttribute('aria-expanded')).toBe('true');
    expect(within(menu).getAllByRole('menuitem').map((i) => i.textContent)).toEqual([expect.stringContaining('this server'), expect.stringContaining('Connect a server')]);
    expect(within(menu).queryByRole('menuitem', { name: /All servers/ })).toBeNull();
  });

  it('the switcher lists this server, connected servers, All servers and Connect a server', async () => {
    const user = userEvent.setup();
    await withPeers([peer(), peer({ id: 'b2', name: 'edge', slug: 'edge', status: 'unreachable', stackCount: 0 })]);
    renderIn(<Sidebar {...props} current="/" />);
    await screen.findByRole('button', { name: /Switch server.*ratsrv01/ });
    const menu = await openMenu(user);
    const items = within(menu).getAllByRole('menuitem');
    expect(items.map((i) => i.textContent)).toEqual([
      expect.stringContaining('ratsrv01'),
      expect.stringContaining('mini-02'),
      expect.stringContaining('edge'),
      expect.stringContaining('All servers'),
      expect.stringContaining('Connect a server'),
    ]);
    expect(within(menu).getByRole('menuitem', { name: /ratsrv01/ }).getAttribute('aria-current')).toBe('true');
    expect(within(menu).getByRole('menuitem', { name: /mini-02/ }).textContent).toContain('4');
    const edge = within(menu).getByRole('menuitem', { name: /edge/ });
    expect(edge.textContent).toContain('unreachable');
    expect(edge.querySelector('[data-tone="danger"]')).toBeTruthy();
    expect(within(menu).getByRole('menuitem', { name: /All servers/ }).textContent).toContain('7');
  });

  it('the switcher follows the unsaved-changes guard and closes after a choice', async () => {
    const user = userEvent.setup();
    const onNavigate = vi.fn();
    await withPeers([peer()]);
    renderIn(<Sidebar {...props} onNavigate={onNavigate} current="/" />);
    await screen.findByRole('button', { name: /Switch server.*ratsrv01/ });
    for (const [name, href] of [[/mini-02/, '/s/mini-02'], [/All servers/, '/all'], [/Connect a server/, '/settings/servers'], [/ratsrv01/, '/']] as const) {
      const menu = await openMenu(user);
      await user.click(within(menu).getByRole('menuitem', { name }));
      expect(onNavigate).toHaveBeenLastCalledWith(href);
      expect(screen.queryByRole('menu')).toBeNull();
    }
    expect(onNavigate).toHaveBeenCalledTimes(4);
    expect(screen.getByTestId('where').textContent).toBe('/');
  });

  it('the switcher navigates through the router when the app supplies no guard, and Escape closes it', async () => {
    const user = userEvent.setup();
    await withPeers([peer()]);
    renderIn(<Sidebar {...props} current="/" />);
    await screen.findByRole('button', { name: /Switch server.*ratsrv01/ });
    await user.click(within(await openMenu(user)).getByRole('menuitem', { name: /mini-02/ }));
    expect(screen.getByTestId('where').textContent).toBe('/s/mini-02');
    await openMenu(user);
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('viewing a peer shows it in the switcher and points every page at it, with Settings staying local', async () => {
    await withPeers([peer()]);
    renderIn(<Sidebar {...props} current="/s/mini-02/containers" />);
    await screen.findByRole('button', { name: /Switch server.*mini-02/ });
    expect(screen.getByRole('link', { name: 'Containers' }).getAttribute('href')).toBe('/s/mini-02/containers');
    expect(screen.getByRole('link', { name: 'Overview' }).getAttribute('href')).toBe('/s/mini-02');
    expect(screen.getByRole('link', { name: 'Activity' }).getAttribute('href')).toBe('/s/mini-02/activity');
    expect(screen.getByRole('link', { name: 'Backups' }).getAttribute('href')).toBe('/s/mini-02/backups');
    expect(current('Containers')).toBe('page');
    expect(current('Overview')).toBeNull();
    expect(screen.getByRole('link', { name: 'Settings' }).getAttribute('href')).toBe('/settings');
  });

  describe('scope banner', () => {
    const shellProps = { onLogout: () => {}, runningOps: [], attention: false, onOpenPalette: () => {} };
    const shell = () => (
      <Shell {...shellProps} username="ana">
        <main>
          <h1>Page</h1>
        </main>
      </Shell>
    );

    it('a connected server screen says which server it is and that actions run there, with a link back', async () => {
      await withPeers([peer({ name: 'NAS', slug: 'nas' })]);
      renderIn(shell(), '/s/nas/containers');
      const banner = await screen.findByRole('status');
      expect(within(banner).getByText('Viewing NAS')).toBeTruthy();
      expect(within(banner).getByText('Actions run on that server.')).toBeTruthy();
      expect(within(banner).getByRole('link', { name: 'Back to this server' }).getAttribute('href')).toBe('/');
    });

    it('is absent on this server, so local screens do not shift', async () => {
      await withPeers([peer({ name: 'NAS', slug: 'nas' })]);
      renderIn(shell(), '/containers');
      await screen.findByRole('heading', { name: 'Page' });
      expect(screen.queryByText(/^Viewing /)).toBeNull();
      expect(screen.queryByRole('link', { name: 'Back to this server' })).toBeNull();
    });

    it('is absent on Settings, which stays local', async () => {
      await withPeers([peer({ name: 'NAS', slug: 'nas' })]);
      renderIn(shell(), '/settings/system');
      await screen.findByRole('heading', { name: 'Page' });
      expect(screen.queryByText(/^Viewing /)).toBeNull();
    });
  });

  it('an unreachable peer is marked in the switcher, and the total counts reachable servers only', async () => {
    const user = userEvent.setup();
    await withPeers([peer({ status: 'unreachable', stackCount: 0 })]);
    renderIn(<Sidebar {...props} current="/" />);
    await screen.findByRole('button', { name: /Switch server.*ratsrv01/ });
    const menu = await openMenu(user);
    const mini = within(menu).getByRole('menuitem', { name: /mini-02/ });
    expect(mini.textContent).toContain('unreachable');
    expect(within(mini).getByRole('img', { name: /unreachable/i })).toBeTruthy();
    expect(within(menu).getByRole('menuitem', { name: /All servers/ }).textContent).toContain('3');
  });

  it('several peers keep added order, after this server', async () => {
    const user = userEvent.setup();
    await withPeers([peer({ id: 'b1', name: 'zeta', slug: 'zeta' }), peer({ id: 'b2', name: 'alpha', slug: 'alpha' })]);
    renderIn(<Sidebar {...props} current="/" />);
    await screen.findByRole('button', { name: /Switch server.*ratsrv01/ });
    const names = within(await openMenu(user)).getAllByRole('menuitem').map((i) => i.textContent ?? '');
    const order = ['ratsrv01', 'zeta', 'alpha'].map((n) => names.findIndex((t) => t.includes(n)));
    expect(order).toEqual([...order].sort((a, b) => a - b));
    expect(order.every((i) => i >= 0)).toBe(true);
  });

  it('a pairing still pending does not turn the switcher or mobile select into the multi-server view', async () => {
    const user = userEvent.setup();
    await withPeers([peer({ status: 'pending' })]);
    renderIn(<Sidebar {...props} current="/" />);
    const menu = await openMenu(user);
    expect(within(menu).queryByRole('menuitem', { name: /mini-02/ })).toBeNull();
    expect(within(menu).queryByRole('menuitem', { name: /All servers/ })).toBeNull();
    cleanup();
    renderIn(<MobileTopBar title="Overview" mono={false} attention={false} />);
    expect(screen.queryByRole('combobox', { name: 'Server' })).toBeNull();
  });

  it('mobile New stack creates on the viewed server, or locally when none is viewed', () => {
    renderIn(<MobileTopBar title="Overview" mono={false} attention={false} />, '/s/mini-02/stacks');
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/s/mini-02/stacks/new');
    cleanup();
    renderIn(<MobileTopBar title="Overview" mono={false} attention={false} />, '/stacks');
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/stacks/new');
  });

  it('the mobile switcher appears only with peers, navigates to /s/{slug}, and reaches All servers and Connect a server', async () => {
    const user = userEvent.setup();
    renderIn(<MobileTopBar title="Overview" mono={false} attention={false} />);
    expect(screen.queryByRole('combobox', { name: 'Server' })).toBeNull();
    cleanup();
    await withPeers([peer()]);
    renderIn(<MobileTopBar title="Overview" mono={false} attention={false} />);
    const select = await screen.findByRole('combobox', { name: 'Server' });
    await user.selectOptions(select, 'mini-02');
    expect(screen.getByTestId('where').textContent).toBe('/s/mini-02');
    await user.selectOptions(select, 'ratsrv01 (this server)');
    expect(screen.getByTestId('where').textContent).toBe('/');
    await user.selectOptions(select, 'All servers');
    expect(screen.getByTestId('where').textContent).toBe('/all');
    await user.selectOptions(select, 'Connect a server');
    expect(screen.getByTestId('where').textContent).toBe('/settings/servers');
  });

  it('the mobile switcher goes through the unsaved-changes guard', async () => {
    const user = userEvent.setup();
    const onNavigate = vi.fn();
    await withPeers([peer()]);
    renderIn(<MobileTopBar title="Overview" mono={false} attention={false} onNavigate={onNavigate} />);
    await user.selectOptions(await screen.findByRole('combobox', { name: 'Server' }), 'mini-02');
    expect(onNavigate).toHaveBeenCalledWith('/s/mini-02');
    expect(screen.getByTestId('where').textContent).toBe('/');
  });

  it('the mobile tab bar links into the viewed server and carries it to More', () => {
    renderIn(<TabBar {...{ attention: false, onOpenPalette: () => {} }} current="/s/mini-02/containers" />);
    expect(screen.getByRole('link', { name: 'Home' }).getAttribute('href')).toBe('/s/mini-02');
    expect(screen.getByRole('link', { name: 'Containers' }).getAttribute('href')).toBe('/s/mini-02/containers');
    expect(screen.getByRole('link', { name: 'Activity' }).getAttribute('href')).toBe('/s/mini-02/activity');
    expect(screen.getByRole('link', { name: 'More' }).getAttribute('href')).toBe('/more?server=mini-02');
    expect(current('Containers')).toBe('page');
  });

  it('the top bar and host block describe the viewed peer, with no second stream for it', async () => {
    const fake = installFakeFetch({
      'GET /api/peers': { body: [peer()] },
      'GET /api/servers/b1/api/stacks': { body: [] },
      'GET /api/servers/b1/api/status': { body: { docker: 'ok', compose: '2.29.0', events: 'connected', metricsDegraded: false } },
      'GET /api/servers/b1/api/host': { body: { hostname: 'mini-02-host', cpu: null, memory: null, disk: null, uptime: null } },
      'GET /api/servers/b1/api/operations?state=running': {
        body: [{ id: 'o1', stack: 'remote-web', action: 'restart', state: 'running', startedAt: '2026-10-19T10:00:00Z' }],
      },
    });
    const stream = installFakeStream({ '/api/servers/b1/api/events': [': ok\n\n'] });
    stopPeers = startPeers({ list: api.peers });
    vi.stubGlobal('matchMedia', () => ({ matches: false, media: '', addEventListener() {}, removeEventListener() {} }));
    try {
      renderIn(
        <Shell {...{ onLogout: () => {}, runningOps: [], attention: false, onOpenPalette: () => {} }} username="ana">
          <Routes>
            <Route path="/s/:server/*" element={<RemoteServer />} />
          </Routes>
        </Shell>,
        '/s/mini-02',
      );
      await screen.findByText('restart · remote-web');
      await screen.findByText('mini-02-host');
      await waitFor(() => expect(screen.getAllByText('live').length).toBeGreaterThan(0));
      expect(stream.requests.filter((r) => r.path === '/api/servers/b1/api/events')).toHaveLength(1);
      expect(stream.requests.filter((r) => r.path === '/api/events')).toHaveLength(0);
    } finally {
      cleanup();
      await sleep(5);
      stream.restore();
      fake.restore();
    }
  });
});
