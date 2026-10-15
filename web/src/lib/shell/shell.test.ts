import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import type { Status } from '../api/types';
import { navigate, route } from '../router';
import { setTheme } from '../stores/appearance';
import { startLive, stopLive } from '../stores/live';
import { breadcrumb } from './breadcrumb';
import Rail from './Rail.svelte';
import TopBar from './TopBar.svelte';

describe('breadcrumb', () => {
  it.each([
    ['/', [{ label: 'Stacks' }]],
    ['/stacks/new', [{ label: 'Stacks', href: '/' }, { label: 'New stack' }]],
    ['/stacks/import', [{ label: 'Stacks', href: '/' }, { label: 'Import' }]],
    ['/stacks/web', [{ label: 'Stacks', href: '/' }, { label: 'web', mono: true }]],
    ['/stacks/my%20stack', [{ label: 'Stacks', href: '/' }, { label: 'my stack', mono: true }]],
    ['/settings', [{ label: 'Settings' }]],
    ['/nope', [{ label: 'Stacks', href: '/' }, { label: 'Not found' }]],
  ])('%s', (path, crumbs) => {
    expect(breadcrumb(path)).toEqual(crumbs);
  });
});

describe('Rail', () => {
  afterEach(() => {
    navigate('/', { replace: true }); // the router store outlives the test
    setTheme('system');
  });

  it('links to the main pages and marks the current one', () => {
    render(Rail, { username: 'ana', onlogout: () => {}, current: '/settings' });
    expect(screen.getByRole('navigation', { name: 'Main' })).toBeTruthy();
    expect(screen.getByRole('link', { name: 'Stackstead' }).getAttribute('href')).toBe('/');
    expect(screen.getByRole('link', { name: 'Stacks' }).getAttribute('href')).toBe('/');
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('href')).toBe('/stacks/new');
    expect(screen.getByRole('link', { name: 'Import' }).getAttribute('href')).toBe('/stacks/import');
    expect(screen.getByRole('link', { name: 'Settings' }).getAttribute('aria-current')).toBe('page');
    expect(screen.getByRole('link', { name: 'Settings' }).getAttribute('title')).toBe('Settings');
    expect(screen.getByRole('link', { name: 'Stacks' }).getAttribute('aria-current')).toBeNull();
  });

  it('keeps Stacks current on a stack detail path but not on create or import', () => {
    const { rerender } = render(Rail, { username: 'ana', onlogout: () => {}, current: '/stacks/web' });
    expect(screen.getByRole('link', { name: 'Stacks' }).getAttribute('aria-current')).toBe('page');
    rerender({ username: 'ana', onlogout: () => {}, current: '/stacks/new' });
    expect(screen.getByRole('link', { name: 'Stacks' }).getAttribute('aria-current')).toBeNull();
    expect(screen.getByRole('link', { name: 'New stack' }).getAttribute('aria-current')).toBe('page');
    rerender({ username: 'ana', onlogout: () => {}, current: '/stacks/import' });
    expect(screen.getByRole('link', { name: 'Stacks' }).getAttribute('aria-current')).toBeNull();
  });

  it('navigates client-side when a link is clicked', async () => {
    render(Rail, { username: 'ana', onlogout: () => {}, current: '/' });
    await userEvent.setup().click(screen.getByRole('link', { name: 'Settings' }));
    expect(get(route).path).toBe('/settings');
  });

  it('theme toggle label flips and calls setTheme', async () => {
    setTheme('dark');
    render(Rail, { username: 'ana', onlogout: () => {}, current: '/' });
    await userEvent.setup().click(screen.getByRole('button', { name: 'Switch to light theme' }));
    expect(screen.getByRole('button', { name: 'Switch to dark theme' })).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Switch to dark theme' }));
    expect(screen.getByRole('button', { name: 'Switch to light theme' })).toBeTruthy();
  });

  it('avatar shows initials and logs out through onlogout', async () => {
    const onlogout = vi.fn();
    render(Rail, { username: 'ana', onlogout, current: '/' });
    const avatar = screen.getByRole('button', { name: 'Log out' });
    expect(avatar.textContent?.trim()).toBe('AN');
    expect(avatar.getAttribute('title')).toBe('ana · Log out');
    await userEvent.setup().click(avatar);
    expect(onlogout).toHaveBeenCalledTimes(1);
  });
});

describe('TopBar', () => {
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
    await new Promise((r) => setTimeout(r, 0));
  }
  beforeEach(() => stopLive());
  afterEach(() => stopLive());

  it('renders the breadcrumb with the last crumb current', () => {
    render(TopBar, { path: '/stacks/web', runningOps: 0 });
    const nav = screen.getByRole('navigation', { name: 'Breadcrumb' });
    expect(nav.querySelector('a')?.getAttribute('href')).toBe('/');
    expect(screen.getByText('web').getAttribute('aria-current')).toBe('page');
  });

  it('shows the running pill only when there are running operations', () => {
    const { rerender } = render(TopBar, { path: '/', runningOps: 0 });
    expect(screen.queryByRole('status')).toBeNull();
    rerender({ path: '/', runningOps: 2 });
    expect(screen.getByRole('status').textContent?.trim()).toBe('2 running');
  });

  it('docker pill reflects version and availability', async () => {
    await withStatus({});
    const { unmount } = render(TopBar, { path: '/', runningOps: 0 });
    expect(screen.getByText('Docker 27.1.1')).toBeTruthy();
    unmount();
    stopLive();
    await withStatus({ dockerVersion: '' });
    const second = render(TopBar, { path: '/', runningOps: 0 });
    expect(screen.getByText('Docker')).toBeTruthy();
    second.unmount();
    stopLive();
    await withStatus({ docker: 'unavailable', dockerVersion: '' });
    render(TopBar, { path: '/', runningOps: 0 });
    expect(screen.getByText('Docker unavailable')).toBeTruthy();
  });

  it('docker pill is neutral before the first status answer', () => {
    render(TopBar, { path: '/', runningOps: 0 });
    const pill = screen.getByRole('group', { name: 'Docker' });
    expect(pill.textContent?.trim()).toBe('Docker');
    expect(pill.querySelector('.bad')).toBeNull();
  });

  it('docker pill title equals its text in every state', async () => {
    for (const [s, text] of [
      [{}, 'Docker 27.1.1'],
      [{ dockerVersion: '' }, 'Docker'],
      [{ docker: 'unavailable', dockerVersion: '' }, 'Docker unavailable'],
    ] as const) {
      stopLive();
      await withStatus(s as Partial<Status>);
      const { unmount } = render(TopBar, { path: '/', runningOps: 0 });
      const pill = screen.getByRole('group', { name: text });
      expect(pill.getAttribute('title')).toBe(text);
      expect(pill.textContent?.trim()).toBe(text);
      unmount();
    }
  });

  it('compose pill shows the version or unavailable', async () => {
    await withStatus({});
    const { unmount } = render(TopBar, { path: '/', runningOps: 0 });
    expect(screen.getByText('compose 2.29.2')).toBeTruthy();
    unmount();
    stopLive();
    await withStatus({ compose: 'unavailable' });
    render(TopBar, { path: '/', runningOps: 0 });
    expect(screen.getByText('compose unavailable')).toBeTruthy();
  });
});
