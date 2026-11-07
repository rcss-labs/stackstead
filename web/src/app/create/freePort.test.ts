import { describe, expect, it } from 'vitest';
import { firstFreePortAfter } from './freePort';

describe('firstFreePortAfter', () => {
  it('returns the next port when it is free', () => {
    expect(firstFreePortAfter(new Set([8096]), 8096)).toBe(8097);
  });

  it('skips taken ports', () => {
    expect(firstFreePortAfter(new Set([8096, 8097, 8098]), 8096)).toBe(8099);
  });

  it('stops at 65535 and reports none', () => {
    expect(firstFreePortAfter(new Set([65535]), 65534)).toBeUndefined();
    expect(firstFreePortAfter(new Set(), 65535)).toBeUndefined();
  });
});
