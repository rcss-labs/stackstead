import { describe, expect, it } from 'vitest';
import { isNewerAvailable } from './tags';

describe('isNewerAvailable', () => {
  it('is false when no latest tag is known', () => {
    expect(isNewerAvailable('1', undefined)).toBe(false);
  });

  it('is false when the latest tag is empty', () => {
    expect(isNewerAvailable('1', '')).toBe(false);
  });

  it('is false when the latest tag equals the current one', () => {
    expect(isNewerAvailable('1.4', '1.4')).toBe(false);
  });

  it('is true when the latest tag differs from the current one', () => {
    expect(isNewerAvailable('1.4', '1.5')).toBe(true);
  });
});
