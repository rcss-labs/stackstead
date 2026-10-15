import { render, screen, within } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import type { ServiceDetail } from '$lib/api/types';
import ServiceCard from './ServiceCard.svelte';

const svc = (over: Partial<ServiceDetail['config']> = {}): ServiceDetail => ({
  name: 'app',
  config: { image: 'nginx:1', limits: {}, ...over },
  readOnly: [],
  containers: [{ id: 'c1', name: 'web-app-1', service: 'app', state: 'running', status: 'Up 2 minutes', health: 'none' }],
});
const props = { stack: 'web', onedit: () => {}, logsHref: '/stacks/web?tab=logs&service=app' };

describe('ServiceCard', () => {
  it('shows ports and volumes as chips, limits as cpus and memory, and dashes when empty', () => {
    render(ServiceCard, { ...props, service: svc({ ports: ['80:80', '443:443'], limits: { cpus: '0.5', memory: '256m' } }) });
    expect(screen.getByText('80:80').classList.contains('chip')).toBe(true);
    expect(screen.getByText('443:443')).toBeTruthy();
    expect(screen.getByText('0.5 cpus · 256m')).toBeTruthy();
    const volumes = screen.getByText('Volumes').nextElementSibling as HTMLElement;
    expect(within(volumes).getByText('—')).toBeTruthy();
  });

  it('links Logs to the preselected service', () => {
    render(ServiceCard, { ...props, service: svc() });
    expect(screen.getByRole('link', { name: 'Logs' }).getAttribute('href')).toBe('/stacks/web?tab=logs&service=app');
  });
});
