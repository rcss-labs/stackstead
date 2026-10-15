import { render, screen } from '@testing-library/svelte';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import Sparkline from '../components/Sparkline.svelte';
import type { Alert, DockerEvent, TopEntry } from '$lib/api/types';
import AlertsCard from './AlertsCard.svelte';
import EventsFeed from './EventsFeed.svelte';
import HostTile from './HostTile.svelte';
import TopConsumersCard from './TopConsumersCard.svelte';

describe('dashboard components', () => {
  it('HostTile renders label, value, sub and bar', () => {
    const { container } = render(HostTile, { tile: { id: 'cpu', label: 'CPU', value: '18.0%', sub: '8 cores', subTone: 'warning', ratio: 0.18 } });
    expect(screen.getByText('CPU')).toBeTruthy();
    expect(screen.getByText('18.0%')).toBeTruthy();
    const sub = screen.getByText('8 cores');
    expect(sub.className).toContain('warning');
    const fill = container.querySelector('.bar > span') as HTMLElement;
    expect(fill.style.width).toBe('18%');
  });

  it('HostTile has no bar without a ratio', () => {
    const { container } = render(HostTile, { tile: { id: 'x', label: 'Uptime', value: '1d' } });
    expect(container.querySelector('.bar')).toBeNull();
  });

  it('Sparkline is aria-hidden and uses the tone', () => {
    const { container } = render(Sparkline, { values: [1, 2, 3], tone: 'info' });
    const svg = container.querySelector('svg')!;
    expect(svg.getAttribute('aria-hidden')).toBe('true');
    expect(svg.getAttribute('viewBox')).toBe('0 0 60 18');
    const path = svg.querySelector('path')!;
    expect(path.getAttribute('stroke')).toBe('var(--info)');
    expect(path.getAttribute('d')).toBe('M0,12L30,6L60,0');
  });

  it('Sparkline draws a flat line when there are no samples', () => {
    const { container } = render(Sparkline, { values: [null, null], tone: 'accent' });
    const path = container.querySelector('path')!;
    expect(path.getAttribute('d')).toBe('M0,17L60,17');
    expect(path.getAttribute('stroke')).toBe('var(--accent)');
  });
});

const alert = (over: Partial<Alert> = {}): Alert => ({ kind: 'unhealthy', severity: 'danger', stack: 'web', message: 'db is unhealthy', since: '2026-10-15T10:00:00Z', ...over });

describe('AlertsCard', () => {
  it('alerts render with links and count in heading', () => {
    render(AlertsCard, { alerts: [alert(), alert({ kind: 'restart_loop', severity: 'warning', stack: 'api', message: 'restarting' })] });
    expect(screen.getByRole('heading', { name: 'Alerts 2' })).toBeTruthy();
    const link = screen.getByRole('link', { name: 'web' });
    expect(link.getAttribute('href')).toBe('/stacks/web');
    expect(screen.getByText('db is unhealthy')).toBeTruthy();
    expect(screen.getByText('unhealthy')).toBeTruthy();
    expect(screen.getAllByRole('listitem')).toHaveLength(2);
  });

  it('alert without stack renders plain text', () => {
    render(AlertsCard, { alerts: [alert({ kind: 'docker_disconnected', stack: undefined, message: 'Docker is unreachable' })] });
    expect(screen.queryByRole('link')).toBeNull();
    expect(screen.getByText('Docker is unreachable')).toBeTruthy();
  });

  it('alert severity is available as text', () => {
    render(AlertsCard, { alerts: [alert({ severity: 'warning' })] });
    expect(screen.getByRole('listitem').textContent).toContain('warning');
  });

  it('alerts not loaded shows no empty text', () => {
    render(AlertsCard, { alerts: undefined });
    expect(screen.queryByText('No alerts.')).toBeNull();
    expect(screen.getByRole('heading', { name: 'Alerts' })).toBeTruthy();
  });

  it('empty alerts message', () => {
    render(AlertsCard, { alerts: [] });
    expect(screen.getByText('No alerts.')).toBeTruthy();
    expect(screen.getByRole('heading', { name: 'Alerts 0' })).toBeTruthy();
  });
});

describe('TopConsumersCard', () => {
  const entry = (container: string, project: string, value: number): TopEntry => ({ container, service: 'svc', project, value });
  const top = {
    cpu: [entry('web-1', 'web', 50), entry('db-1', 'web', 25)],
    mem: [entry('cache-1', 'kv', 2 * 1024 ** 3), entry('web-1', 'web', 1024 ** 3)],
  };

  it('top consumers toggle switches metric and bars scale to top', async () => {
    const { container } = render(TopConsumersCard, { top });
    expect(screen.getByRole('button', { name: 'CPU' }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByText('50.0%')).toBeTruthy();
    const fills = () => [...container.querySelectorAll<HTMLElement>('.bar > span')].map((f) => f.style.width);
    expect(fills()).toEqual(['100%', '50%']);
    await userEvent.setup().click(screen.getByRole('button', { name: 'Memory' }));
    expect(screen.getByRole('button', { name: 'Memory' }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByText('2.0 GiB')).toBeTruthy();
    expect(screen.getByText('cache-1 · kv')).toBeTruthy();
    expect(fills()).toEqual(['100%', '50%']);
  });

  it('top consumers label uses the stack name, falling back to project, and omits an empty project', () => {
    render(TopConsumersCard, {
      top: { cpu: [entry('web-1', 'proj-a', 3), entry('x-1', 'other', 2), entry('lone', '', 1)], mem: [] },
      stackNames: { 'proj-a': 'alpha' },
    });
    expect(screen.getByText('web-1 · alpha')).toBeTruthy();
    expect(screen.getByText('x-1 · other')).toBeTruthy();
    expect(screen.getByText('lone')).toBeTruthy();
  });

  it('top consumers not loaded shows no empty text', () => {
    render(TopConsumersCard, { top: undefined });
    expect(screen.queryByText('No samples yet.')).toBeNull();
  });

  it('top consumers empty', () => {
    render(TopConsumersCard, { top: { cpu: [], mem: [] } });
    expect(screen.getByText('No samples yet.')).toBeTruthy();
  });
});

describe('EventsFeed', () => {
  const ev = (action: string, container: string, over: Partial<DockerEvent> = {}): DockerEvent => ({
    action, container, project: 'p', service: 's', image: 'img', time: '2026-10-15T10:00:00Z', ...over,
  });

  it('events feed prepends newest and caps at 50', () => {
    const events = Array.from({ length: 50 }, (_, i) => ev('start', `c-${50 - i}`));
    render(EventsFeed, { props: { events, connection: 'open' } });
    const items = screen.getAllByRole('listitem');
    expect(items).toHaveLength(50);
    expect(items[0].textContent).toContain('c-50');
  });

  it('events feed status shows reconnecting when not open', () => {
    const { rerender } = render(EventsFeed, { props: { events: [], connection: 'open' } });
    expect(screen.getByText('live')).toBeTruthy();
    expect(screen.queryByText('reconnecting')).toBeNull();
    rerender({ events: [], connection: 'reconnecting' });
    return Promise.resolve().then(() => {
      expect(screen.getByText('reconnecting')).toBeTruthy();
      expect(screen.queryByText('live')).toBeNull();
    });
  });

  it('events feed is not a live region', () => {
    const { container } = render(EventsFeed, { props: { events: [ev('start', 'a')], connection: 'open' } });
    expect(container.querySelector('[aria-live], [role="log"], [role="status"], [role="alert"]')).toBeNull();
    expect(screen.getByText('start').className).toContain('success');
  });

  it('events feed empty', () => {
    render(EventsFeed, { props: { events: [], connection: 'open' } });
    expect(screen.getByText('Waiting for events…')).toBeTruthy();
  });

  it('time is zero-padded 24h local, midnight is 00', () => {
    const d = new Date(2026, 9, 15, 0, 5, 9);
    render(EventsFeed, { props: { events: [ev('start', 'a', { time: d.toISOString() })], connection: 'open' } });
    expect(screen.getByText('00:05:09')).toBeTruthy();
  });

  it('action cell has a title with the full text', () => {
    render(EventsFeed, { props: { events: [ev('health_status', 'a', { health: 'unhealthy' })], connection: 'open' } });
    expect(screen.getByText('unhealthy').getAttribute('title')).toBe('unhealthy');
  });

  it('die shows exit code', () => {
    render(EventsFeed, { props: { events: [ev('die', 'web-1', { exitCode: 137 })], connection: 'open' } });
    expect(screen.getByText('web-1 exit 137')).toBeTruthy();
    expect(screen.getByText('die').className).toContain('danger');
  });
});
