import { cleanup, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { HealthHistory } from '$lib/api/types';
import { timelineLanes } from '$lib/stacks/timeline';
import { HealthTimeline } from './HealthTimeline';

afterEach(cleanup);

const NOW = 100_000;
const history: HealthHistory = {
  from: NOW - 3600,
  to: NOW,
  services: [
    { service: 'app', restarts: 0, runs: [{ state: 'healthy', from: NOW - 3600, to: NOW }] },
    {
      service: 'db',
      restarts: 3,
      runs: [
        { state: 'healthy', from: NOW - 3600, to: NOW - 300 },
        { state: 'unhealthy', from: NOW - 300, to: NOW },
      ],
    },
  ],
};

function mount(over: { history?: HealthHistory; services?: string[]; selected?: string; onSelect?: (s: string) => void } = {}) {
  const lanes = timelineLanes(over.history ?? history, over.services ?? ['app', 'db'], NOW);
  return render(<HealthTimeline lanes={lanes} selected={over.selected} onSelect={over.onSelect ?? (() => {})} />);
}
const region = () => screen.getByRole('region', { name: 'Health · last 60 min' });
const bar = (name: RegExp) => within(region()).getByRole('img', { name });

describe('health timeline', () => {
  it('renders one lane per service in order with the legend', () => {
    mount();
    const buttons = within(region()).getAllByRole('button');
    expect(buttons.map((b) => b.textContent)).toEqual(['app', 'db↻ 3']);
    expect(region().textContent).toContain('healthyrestartunhealthydown');
    expect(region().textContent).toContain('−60m');
  });

  it('segments carry their weight, tone and tooltip', () => {
    mount();
    const spans = Array.from(bar(/^db:/).children) as HTMLElement[];
    expect(spans.map((s) => s.title)).toEqual(['healthy · 55 min', 'unhealthy · 5 min']);
    expect(spans.map((s) => parseFloat(s.style.flex))).toEqual([3300, 300]);
    expect(spans.map((s) => s.dataset.tone)).toEqual(['success', 'danger']);
  });

  it('only a trailing danger run pulses', () => {
    mount();
    const db = Array.from(bar(/^db:/).children) as HTMLElement[];
    expect(db.map((s) => s.dataset.pulse)).toEqual([undefined, 'true']);
    const app = Array.from(bar(/^app:/).children) as HTMLElement[];
    expect(app.some((s) => s.dataset.pulse)).toBe(false);
  });

  it('a service missing from the response is one outlined nodata run', () => {
    mount({ services: ['app', 'db', 'cache'] });
    const spans = Array.from(bar(/^cache:/).children) as HTMLElement[];
    expect(spans).toHaveLength(1);
    expect(spans[0]!.dataset.tone).toBe('nodata');
    expect(spans[0]!.title).toBe('no data · 60 min');
  });

  it('the bar is an image with a readable summary', () => {
    mount();
    expect(screen.getByRole('img', { name: 'db: healthy 55 min, unhealthy 5 min' })).toBeTruthy();
    expect(screen.getByRole('img', { name: 'app: healthy 60 min' })).toBeTruthy();
  });

  it('restart count shows only above zero with its title', () => {
    mount();
    expect(screen.getByTitle('restarted 3 times in the last 60 min').textContent).toBe('↻ 3');
    expect(screen.queryAllByTitle(/restarted/)).toHaveLength(1);
  });

  it('is not a live region', () => {
    mount();
    expect(region().closest('[aria-live]')).toBeNull();
    expect(region().querySelector('[aria-live],[role="status"],[role="alert"]')).toBeNull();
  });

  it('clicking a name selects the service and marks it pressed', async () => {
    const onSelect = vi.fn();
    mount({ selected: 'db', onSelect });
    expect(screen.getByRole('button', { name: /^db/ }).getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByRole('button', { name: /^app/ }).getAttribute('aria-pressed')).toBe('false');
    await userEvent.setup().click(screen.getByRole('button', { name: /^app/ }));
    expect(onSelect).toHaveBeenCalledWith('app');
  });
});
