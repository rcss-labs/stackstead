import { describe, expect, it } from 'vitest';
import { lifecycleDisabledReason, type LifecycleInput } from './lifecycle';

const healthy: LifecycleInput['live'] = { compose: '2.29.0', docker: 'ok', events: 'connected' };

describe('lifecycleDisabledReason', () => {
  it('is undefined when everything is healthy', () => {
    expect(lifecycleDisabledReason({ live: healthy, status: 'running' })).toBeUndefined();
  });

  it.each([
    ['compose unavailable', { live: { ...healthy, compose: 'unavailable' } }, 'docker compose plugin not found'],
    ['docker unavailable', { live: { ...healthy, docker: 'unavailable' } }, 'Docker is unreachable'],
    ['events disconnected', { live: { ...healthy, events: 'disconnected' } }, 'Docker is unreachable'],
    ['parse error', { live: healthy, parseError: 'bad yaml' }, 'the compose file cannot be parsed'],
    ['status unavailable', { live: healthy, status: 'unavailable' }, 'the compose file cannot be parsed'],
    ['busy', { live: healthy, busy: true }, 'an operation is already running'],
  ] as const)('%s', (_name, input, expected) => {
    expect(lifecycleDisabledReason(input as LifecycleInput)).toBe(expected);
  });

  it('applies precedence: compose, docker, parse, busy', () => {
    const all = { live: { compose: 'unavailable', docker: 'unavailable', events: 'disconnected' }, parseError: 'x', busy: true } as LifecycleInput;
    expect(lifecycleDisabledReason(all)).toBe('docker compose plugin not found');
    expect(lifecycleDisabledReason({ ...all, live: { ...healthy, docker: 'unavailable' } })).toBe('Docker is unreachable');
    expect(lifecycleDisabledReason({ ...all, live: healthy })).toBe('the compose file cannot be parsed');
    expect(lifecycleDisabledReason({ live: healthy, busy: true })).toBe('an operation is already running');
  });
});
