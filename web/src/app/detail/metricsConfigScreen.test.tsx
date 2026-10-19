import { cleanup, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { installHarness, mountDetail, removeHarness, type Harness } from './detailTestKit';

describe('stack detail Metrics and Config tabs', () => {
  let h: Harness;
  beforeEach(() => {
    h = installHarness();
  });
  afterEach(async () => {
    cleanup();
    await removeHarness(h);
  });

  it('?tab=metrics shows the service metrics and shares the live stream', async () => {
    h.fake.calls.length = 0;
    mountDetail(h, '/stacks/web?service=app&tab=metrics');
    expect(await screen.findByRole('region', { name: 'Resource metrics' })).toBeTruthy();
    await waitFor(() => expect(h.liveRequests()).toHaveLength(1));
    await new Promise((r) => setTimeout(r, 20));
    expect(h.liveRequests()).toHaveLength(1);
    expect(screen.queryAllByRole('figure')).toHaveLength(0); // no samples seeded: the empty state
  });

  it('?tab=metrics with history renders the four charts on the real page', async () => {
    h.metricsRes = { body: { points: [{ time: '2026-10-14T10:00:00Z', service: 'app', cpu: 3, mem: 10, memLimit: 100 }] } };
    mountDetail(h, '/stacks/web?service=app&tab=metrics');
    const region = await screen.findByRole('region', { name: 'Resource metrics' });
    await waitFor(() => expect(within(region).getAllByRole('figure')).toHaveLength(4));
    for (const title of ['CPU · 1h', 'Memory · 1h', 'Network · 1h', 'Disk I/O · 1h']) expect(within(region).getByText(title)).toBeTruthy();
  });

  it('legacy ?tab=metrics without a service selects the first service\'s Metrics tab', async () => {
    mountDetail(h, '/stacks/web?tab=metrics');
    expect((await screen.findByRole('tab', { name: 'Metrics' })).getAttribute('aria-selected')).toBe('true');
    expect(screen.getByRole('heading', { level: 2, name: 'app' })).toBeTruthy();
    expect(await screen.findByRole('region', { name: 'Resource metrics' })).toBeTruthy();
  });

  it('?tab=config fetches the compose file once and shows the block', async () => {
    h.composeRes = { body: { content: 'services:\n  app:\n    image: nginx:1\n  db:\n    image: postgres:16\n', hash: 'c1' } };
    mountDetail(h, '/stacks/web?service=app&tab=config');
    await waitFor(() => expect(document.querySelector('pre')?.textContent).toBe('  app:\n    image: nginx:1'));
    expect(h.calls('GET', '/api/stacks/web/compose')).toBe(1);
    await userEvent.setup().click(screen.getByRole('button', { name: 'Edit YAML' }));
    await waitFor(() => expect(screen.getByTestId('location').textContent).toBe('/stacks/web?tab=compose'));
    expect(await screen.findByText('⌘S / Ctrl+S to save')).toBeTruthy();
  });
});
