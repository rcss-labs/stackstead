import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SeriesPoint } from '$lib/metrics/series';
import type { DetailItem } from '$lib/stacks/detailLocation';
import { appService, dbService, installHarness, mountDetail, removeHarness, stackDetail, type Harness } from './detailTestKit';
import { ServiceChips, ServiceRail } from './ServiceRail';

const point = (over: Partial<SeriesPoint> = {}): SeriesPoint => ({ t: 1, cpu: 0, mem: 0, memLimit: 0, ...over });
const user = () => userEvent.setup();

describe('service rail', () => {
  const services = [appService(), dbService()];
  const props = (over: Partial<Parameters<typeof ServiceRail>[0]> = {}) => ({
    services,
    selected: { kind: 'service', name: 'app' } as DetailItem,
    operationsCount: 2,
    latest: new Map<string, SeriesPoint>(),
    onSelect: () => {},
    ...over,
  });
  const renderRail = (over: Partial<Parameters<typeof ServiceRail>[0]> = {}) =>
    render(
      <MemoryRouter>
        <ServiceRail {...props(over)} />
        <ServiceChips {...props(over)} />
      </MemoryRouter>,
    );
  const nav = () => screen.getByRole('navigation', { name: 'Services' });

  afterEach(() => cleanup());

  it('lists services with LED, name, state and cpu, then the stack items with the operations count', () => {
    renderRail();
    const rail = within(nav());
    expect(rail.getByRole('button', { name: /^app\b/ }).textContent).toContain('Up 2 minutes');
    // The state line is the container status alone, as in the design; health shows in the LED.
    expect(rail.getByRole('button', { name: /^db\b/ }).textContent).toContain('Up 2 minutes');
    expect(rail.getByRole('button', { name: /^db\b/ }).textContent).not.toContain('healthy');
    expect(rail.getByRole('button', { name: /^Compose file/ }).textContent).toBe('Compose file yaml');
    const operations = rail.getByRole('button', { name: /^Operations/ });
    expect(operations.textContent).toBe('Operations 2');
    expect(operations.getAttribute('data-item')).toBe('operations');
    expect(rail.getAllByText('—').length).toBeGreaterThanOrEqual(2); // no cpu yet
  });

  it('the selected service is current and cpu bars scale to the live value', () => {
    renderRail({
      selected: { kind: 'service', name: 'db' },
      latest: new Map([['db', point({ cpu: 42 })], ['app', point({ cpu: 12.5 })]]),
    });
    const rail = within(nav());
    expect(rail.getByRole('button', { name: /^db\b/ }).getAttribute('aria-current')).toBe('true');
    expect(rail.getByRole('button', { name: /^app\b/ }).getAttribute('aria-current')).toBeNull();
    const app = rail.getByRole('button', { name: /^app\b/ });
    expect(within(app).getByText('12.5%')).toBeTruthy();
    expect(app.querySelector<HTMLElement>('[data-cpu-fill]')!.style.width).toBe('12.5%');
    expect(rail.getByRole('button', { name: /^db\b/ }).querySelector<HTMLElement>('[data-cpu-fill]')!.style.width).toBe('42%');
  });

  it('caps the cpu bar at 100%', () => {
    renderRail({ latest: new Map([['app', point({ cpu: 250 })]]) });
    expect(within(nav()).getByRole('button', { name: /^app\b/ }).querySelector<HTMLElement>('[data-cpu-fill]')!.style.width).toBe('100%');
  });

  it('marks Compose file and Operations as current when selected', () => {
    renderRail({ selected: { kind: 'compose' } });
    const rail = within(nav());
    expect(rail.getByRole('button', { name: /^Compose file/ }).getAttribute('aria-current')).toBe('true');
    expect(rail.getByRole('button', { name: /^app\b/ }).getAttribute('aria-current')).toBeNull();
  });

  it('footer totals show dashes until samples then totals', () => {
    const view = renderRail();
    const totals = () => within(nav()).getByText('CPU').closest('dl')!;
    expect(Array.from(totals().querySelectorAll('dd')).map((d) => d.textContent)).toEqual(['—', '—', '—']);
    view.unmount();
    renderRail({
      latest: new Map([
        ['app', point({ cpu: 10, mem: 1048576, netRx: 1024, netTx: 2048 })],
        ['db', point({ cpu: 5.5 })],
      ]),
    });
    expect(Array.from(totals().querySelectorAll('dd')).map((d) => d.textContent)).toEqual(['15.5%', '1.0 MiB', '1.0 KiB/s · 2.0 KiB/s']);
  });

  it('mobile chips mirror the rail items', () => {
    renderRail({ selected: { kind: 'service', name: 'db' } });
    const chips = within(screen.getByLabelText('Service chips'));
    expect(chips.getByRole('button', { name: 'app' }).getAttribute('aria-pressed')).toBe('false');
    expect(chips.getByRole('button', { name: 'db' }).getAttribute('aria-pressed')).toBe('true');
    expect(chips.getByRole('button', { name: 'Compose' }).getAttribute('aria-pressed')).toBe('false');
    expect(chips.getByRole('button', { name: 'Operations' }).getAttribute('aria-pressed')).toBe('false');
  });

  it('clicks on rail items and chips report the item', async () => {
    const picked: DetailItem[] = [];
    renderRail({ onSelect: (item) => picked.push(item) });
    await user().click(within(nav()).getByRole('button', { name: /^db\b/ }));
    await user().click(within(nav()).getByRole('button', { name: /^Compose file/ }));
    await user().click(within(screen.getByLabelText('Service chips')).getByRole('button', { name: 'Operations' }));
    expect(picked).toEqual([{ kind: 'service', name: 'db' }, { kind: 'compose' }, { kind: 'operations' }]);
  });

  it('renders the add slot beside the Services label', () => {
    renderRail({ addSlot: <button type="button">+ Add</button> });
    expect(within(nav()).getByRole('button', { name: '+ Add' })).toBeTruthy();
  });
});

describe('stack detail rail and live metrics', () => {
  let h: Harness;
  beforeEach(() => {
    h = installHarness();
    // The media store keeps its last value when matchMedia disappears, so every test states its width.
    vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener: () => {}, removeEventListener: () => {} }));
  });
  afterEach(async () => {
    cleanup();
    vi.unstubAllGlobals();
    await removeHarness(h);
  });

  const here = () => screen.getByTestId('location').textContent;
  const railNav = () => screen.findByRole('navigation', { name: 'Services' });
  const wide = () => vi.stubGlobal('matchMedia', () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} }));
  const settle = () => new Promise((r) => setTimeout(r, 30));

  it('clicking a service, Compose file or Operations navigates through the guard', async () => {
    mountDetail(h, '/stacks/web');
    const rail = within(await railNav());
    await user().click(rail.getByRole('button', { name: /^db\b/ }));
    await waitFor(() => expect(here()).toBe('/stacks/web?service=db'));
    await user().click(rail.getByRole('button', { name: /^Compose file/ }));
    await waitFor(() => expect(here()).toBe('/stacks/web?tab=compose'));
    await user().click(rail.getByRole('button', { name: /^Operations/ }));
    await waitFor(() => expect(here()).toBe('/stacks/web?tab=operations'));
  });

  it('the mobile chips navigate too', async () => {
    mountDetail(h, '/stacks/web');
    await railNav();
    await user().click(within(screen.getByLabelText('Service chips')).getByRole('button', { name: 'db' }));
    await waitFor(() => expect(here()).toBe('/stacks/web?service=db'));
  });

  it('the rail shows the operations count', async () => {
    mountDetail(h, '/stacks/web');
    const operations = within(await railNav()).getByRole('button', { name: /^Operations/ });
    await waitFor(() => expect(operations.textContent).toBe('Operations 2'));
  });

  it('under a parse error the rail has no services and Compose file is the selected item', async () => {
    h.stackRes = { body: stackDetail({ status: 'unavailable', parseError: 'yaml: line 3: bad', services: [] }) };
    mountDetail(h, '/stacks/web');
    const rail = within(await railNav());
    expect(rail.getByRole('button', { name: /^Compose file/ }).getAttribute('aria-current')).toBe('true');
    expect(rail.queryByRole('button', { name: /^app\b/ })).toBeNull();
    expect(screen.queryByRole('tablist', { name: 'Service sections' })).toBeNull();
  });

  it('live samples reach the rail cpu value', async () => {
    wide();
    mountDetail(h, '/stacks/web');
    await waitFor(() => expect(h.liveRequests()).toHaveLength(1));
    const sample = { time: '2026-10-14T10:00:00Z', service: 'app', container: 'abc', cpu: 12.5, mem: 1, memLimit: 2, netRx: 0, netTx: 0, blkRead: 0, blkWrite: 0 };
    h.stream.push('/api/stacks/web/metrics/live', `event: sample\ndata: ${JSON.stringify(sample)}\n\n`);
    const app = within(await railNav()).getByRole('button', { name: /^app\b/ });
    await waitFor(() => expect(within(app).getByText('12.5%')).toBeTruthy(), { timeout: 3000 });
  });

  describe('live stream', () => {
    it('no live stream when narrow and not on Metrics', async () => {
      mountDetail(h, '/stacks/web');
      await railNav();
      await settle();
      expect(h.liveRequests()).toHaveLength(0);
    });

    it('one stream shared by the rail and the Metrics tab', async () => {
      wide();
      mountDetail(h, '/stacks/web');
      await railNav();
      await waitFor(() => expect(h.liveRequests()).toHaveLength(1));
      await user().click(screen.getByRole('tab', { name: 'Metrics' }));
      await waitFor(() => expect(here()).toBe('/stacks/web?service=app&tab=metrics'));
      await settle();
      expect(h.liveRequests()).toHaveLength(1);
      expect(h.liveRequests()[0]!.signal!.aborted).toBe(false);
    });

    it('stream closes when leaving both', async () => {
      mountDetail(h, '/stacks/web?tab=metrics');
      await waitFor(() => expect(h.liveRequests()).toHaveLength(1));
      const signal = h.liveRequests()[0]!.signal!;
      await user().click(await screen.findByRole('tab', { name: 'Overview' }));
      await waitFor(() => expect(signal.aborted).toBe(true));
    });

    it('stream closes when the page is destroyed', async () => {
      wide();
      const { unmount } = mountDetail(h, '/stacks/web');
      await waitFor(() => expect(h.liveRequests()).toHaveLength(1));
      const signal = h.liveRequests()[0]!.signal!;
      unmount();
      await waitFor(() => expect(signal.aborted).toBe(true));
    });

    it('navigating to another stack closes the stream and opens that stack\'s own', async () => {
      wide();
      mountDetail(h, '/stacks/web');
      await waitFor(() => expect(h.liveRequests()).toHaveLength(1));
      const signal = h.liveRequests()[0]!.signal!;
      h.goTo('/stacks/db');
      await waitFor(() => expect(signal.aborted).toBe(true));
      await waitFor(() => expect(h.stream.requests.filter((r) => r.path.startsWith('/api/stacks/db/metrics/live'))).toHaveLength(1));
    });

    it('no live stream while the stack is loading, failed to load, or not found', async () => {
      wide();
      let release!: () => void;
      h.stackGate = new Promise<void>((r) => (release = r));
      const first = mountDetail(h, '/stacks/web');
      await settle();
      expect(h.liveRequests()).toHaveLength(0);
      release();
      await railNav();
      first.unmount();
      const before = h.liveRequests().length;

      h.stackGate = undefined;
      h.stackRes = { status: 500, body: { error: 'boom' } };
      const failing = mountDetail(h, '/stacks/web');
      await screen.findByText(/Could not load the stack/);
      await settle();
      expect(h.liveRequests().length).toBe(before);
      failing.unmount();

      h.stackRes = { status: 404, body: { error: 'stack not found' } };
      mountDetail(h, '/stacks/web');
      await screen.findByText(/no stack named/i);
      await settle();
      expect(h.liveRequests().length).toBe(before);
    });

    it('opens one stream when the window widens and closes it when it narrows again', async () => {
      const listeners = new Set<(e: { matches: boolean }) => void>();
      vi.stubGlobal('matchMedia', () => ({
        matches: false,
        addEventListener: (_: string, fn: (e: { matches: boolean }) => void) => listeners.add(fn),
        removeEventListener: (_: string, fn: (e: { matches: boolean }) => void) => listeners.delete(fn),
      }));
      mountDetail(h, '/stacks/web');
      await railNav();
      expect(h.liveRequests()).toHaveLength(0);
      listeners.forEach((fn) => fn({ matches: true }));
      await waitFor(() => expect(h.liveRequests()).toHaveLength(1));
      const signal = h.liveRequests()[0]!.signal!;
      listeners.forEach((fn) => fn({ matches: false }));
      await waitFor(() => expect(signal.aborted).toBe(true));
      expect(h.liveRequests()).toHaveLength(1);
    });

    it('no live stream under a parse error', async () => {
      wide();
      h.stackRes = { body: stackDetail({ status: 'unavailable', parseError: 'bad yaml', services: [] }) };
      mountDetail(h, '/stacks/web');
      await railNav();
      await settle();
      expect(h.liveRequests()).toHaveLength(0);
    });
  });
});
