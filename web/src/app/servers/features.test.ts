import { describe, expect, it } from 'vitest';
import { ALL_FEATURES, FEATURE_COMPOSE_RESTORE, FEATURE_DISK_FORECAST } from './ServerContext';

describe('server features', () => {
  it('uses the names the Go server advertises and counts them for this server', () => {
    expect(FEATURE_DISK_FORECAST).toBe('disk-forecast');
    expect(FEATURE_COMPOSE_RESTORE).toBe('compose-restore');
    expect(ALL_FEATURES).toEqual(expect.arrayContaining([FEATURE_DISK_FORECAST, FEATURE_COMPOSE_RESTORE]));
  });
});
