import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { HostSnapshot, Operation, Status } from '$lib/api/types';
import { setTheme } from '$lib/stores/appearance';
import { startHost } from '$lib/stores/host';
import { startLive, stopLive } from '$lib/stores/live';
import { HostBlock } from './HostBlock';
import { MobileTopBar } from './MobileTopBar';
import { Shell } from './Shell';
import { Sidebar } from './Sidebar';
import { TabBar } from './TabBar';
import { TopBar } from './TopBar';
import { pageTitle } from './nav';

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
    expect(screen.getByRole('link', { name: 'Stackstead' }).getAttribute('href')).toBe('/');
    expect(screen.getByRole('link', { name: 'Stacks' }).getAttribute('href')).toBe('/');
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/stacks/new');
    expect(screen.getByRole('link', { name: 'Import' }).getAttribute('href')).toBe('/stacks/import');
    expect(current('Settings')).toBe('page');
    expect(screen.getByRole('link', { name: 'Settings' }).getAttribute('title')).toBe('Settings');
    expect(current('Stacks')).toBeNull();
  });

  it('keeps Stacks current on a stack detail path but not on create or import', () => {
    const { rerender } = renderIn(<Sidebar {...sidebarProps} current="/stacks/web" />);
    const again = (path: string) =>
      rerender(
        <MemoryRouter>
          <Sidebar {...sidebarProps} current={path} />
        </MemoryRouter>,
      );
    expect(current('Stacks')).toBe('page');
    again('/stacks/new');
    expect(current('Stacks')).toBeNull();
    expect(current('New stack')).toBe('page');
    again('/stacks/import');
    expect(current('Stacks')).toBeNull();
  });

  it('has a Containers item after Stacks, current on the list and on a container page only', () => {
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
    expect(names.indexOf('Containers')).toBe(names.indexOf('Stacks') + 1);
    const item = screen.getByRole('link', { name: 'Containers' });
    expect(item.getAttribute('href')).toBe('/containers');
    expect(item.getAttribute('title')).toBe('Containers');
    expect(current('Containers')).toBe('page');
    expect(current('Stacks')).toBeNull();
    again('/containers/redis');
    expect(current('Containers')).toBe('page');
    again('/');
    expect(current('Containers')).toBeNull();
  });

  it('places Networks after Containers and marks only its exact route current', () => {
    const { rerender } = renderIn(<Sidebar {...sidebarProps} current="/networks" />);
    const names = screen.getAllByRole('link').map((a) => a.getAttribute('aria-label'));
    expect(names.indexOf('Networks')).toBe(names.indexOf('Containers') + 1);
    const item = screen.getByRole('link', { name: 'Networks' });
    expect(item.getAttribute('href')).toBe('/networks');
    expect(item.getAttribute('title')).toBe('Networks');
    expect(current('Networks')).toBe('page');
    expect(item.querySelector('path')?.getAttribute('d')).toBeTruthy();
    for (const path of ['/networks/other', '/containers', '/']) {
      rerender(
        <MemoryRouter>
          <Sidebar {...sidebarProps} current={path} />
        </MemoryRouter>,
      );
      expect(current('Networks')).toBeNull();
    }
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

  it('avatar shows initials and logs out through onLogout', async () => {
    const onLogout = vi.fn();
    renderIn(<Sidebar {...sidebarProps} username="ana" onLogout={onLogout} current="/" />);
    const avatar = screen.getByRole('button', { name: 'Log out' });
    expect(avatar.textContent?.trim()).toBe('AN');
    expect(avatar.getAttribute('title')).toBe('ana · Log out');
    await userEvent.setup().click(avatar);
    expect(onLogout).toHaveBeenCalledTimes(1);
  });

  it('shows the attention LED on Stacks only when attention is true', () => {
    const { rerender } = renderIn(<Sidebar {...sidebarProps} attention={false} current="/" />);
    const led = () => screen.getByRole('link', { name: /^Stacks(,|$)/ }).querySelector('[data-tone="danger"]');
    expect(led()).toBeNull();
    rerender(
      <MemoryRouter>
        <Sidebar {...sidebarProps} attention current="/" />
      </MemoryRouter>,
    );
    expect(led()?.getAttribute('data-pulse')).toBe('true');
    expect(screen.getByRole('link', { name: 'Containers' }).querySelector('[data-tone="danger"]')).toBeNull();
  });

  it('announces the attention LED as part of the Stacks link name', () => {
    renderIn(<Sidebar {...sidebarProps} attention current="/" />);
    expect(screen.getByRole('link', { name: /Stacks.*needs attention/i })).toBeTruthy();
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
    ['/', 'Stacks', false],
    ['/containers', 'Containers', false],
    ['/networks', 'Networks', false],
    ['/storage', 'Storage', false],
    ['/settings', 'Settings', false],
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

  it('under 760px the sidebar is replaced by the mobile top bar and tab bar with one Main landmark', () => {
    stubViewport(true);
    renderIn(
      <Shell {...shellProps} username="ana">
        <main>
          <h1>Stacks</h1>
        </main>
      </Shell>,
    );
    expect(screen.getAllByRole('navigation', { name: 'Main' })).toHaveLength(1);
    expect(screen.queryByRole('button', { name: 'Log out' })).toBeNull();
    expect(screen.getByRole('button', { name: 'Search or run a command' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/stacks/new');
    expect(screen.getAllByRole('main')).toHaveLength(1);
    const tabs = within(screen.getByRole('navigation', { name: 'Main' })).getAllByRole('link');
    expect(tabs.map((a) => a.getAttribute('aria-label'))).toEqual(['Stacks', 'Containers', 'Storage', 'Networks', 'Settings']);
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
    expect(within(screen.getByRole('navigation', { name: 'Main' })).getAllByRole('link')).toHaveLength(5);
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
    expect(screen.getByRole('button', { name: 'Log out' })).toBeTruthy();
    expect(screen.queryByRole('link', { name: 'Settings' })?.closest('nav')).toBe(screen.getByRole('navigation', { name: 'Main' }));
  });

  it('tab bar marks the current page and shows the alert LED on Stacks', () => {
    renderIn(<TabBar current="/containers/redis" attention />);
    expect(current('Containers')).toBe('page');
    expect(current(/^Stacks(,|$)/)).toBeNull();
    expect(screen.getByRole('link', { name: /^Stacks(,|$)/ }).querySelector('[data-tone="danger"]')).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Containers' }).querySelector('[data-tone="danger"]')).toBeNull();
  });

  it('tab bar and mobile top bar announce the attention LED', () => {
    renderIn(
      <>
        <TabBar current="/" attention />
        <MobileTopBar title="Stacks" mono={false} attention onOpenPalette={() => {}} />
      </>,
    );
    expect(screen.getByRole('link', { name: /Stacks.*needs attention/i })).toBeTruthy();
    expect(screen.getAllByRole('img', { name: 'Needs attention' })).toHaveLength(1);
  });

  it('mobile title is mono for an entity route', () => {
    const { rerender } = renderIn(<MobileTopBar title="redis" mono attention={false} onOpenPalette={() => {}} />);
    expect(screen.queryByRole('heading')).toBeNull();
    expect(screen.getByText('redis').hasAttribute('data-mono')).toBe(true);
    rerender(
      <MemoryRouter>
        <MobileTopBar title="Networks" mono={false} attention onOpenPalette={() => {}} />
      </MemoryRouter>,
    );
    expect(screen.getByText('Networks').hasAttribute('data-mono')).toBe(false);
  });
});
