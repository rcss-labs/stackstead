import { describe, expect, it } from 'vitest';
import { appService } from './detailTestKit';
import { serviceCause } from './serviceCause';

const restarting = (status: string, memory?: string) =>
  appService({
    config: { image: 'x', limits: memory ? { memory } : {} },
    containers: [{ id: 'a', name: 'web-app-1', service: 'app', state: 'restarting', status, health: 'none' }],
  });

describe('serviceCause', () => {
  it('offers to raise the memory limit for a restart loop killed with 137', () => {
    expect(serviceCause(restarting('Restarting (137) 4 seconds ago', '256m'))?.raiseLimit).toBe(true);
  });

  it('keeps the generic restart hint when the code is not 137 or there is no limit', () => {
    expect(serviceCause(restarting('Restarting (1) 4 seconds ago', '256m'))?.raiseLimit).toBe(false);
    expect(serviceCause(restarting('Restarting (137) 4 seconds ago'))?.raiseLimit).toBe(false);
    expect(serviceCause(restarting('Restarting'))?.title).toBe('Restarting repeatedly');
  });
});
