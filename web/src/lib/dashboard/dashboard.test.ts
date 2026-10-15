import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import Sparkline from '../components/Sparkline.svelte';
import HostTile from './HostTile.svelte';

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
