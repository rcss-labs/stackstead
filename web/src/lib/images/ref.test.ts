import { describe, expect, it } from 'vitest';
import { formatLibraryRef, parseLibraryRef } from './ref';

describe('library references', () => {
  it('round-trips a formatted reference', () => {
    const ref = formatLibraryRef('0123456789ab', 'web-app', '1.2');
    expect(ref).toBe('stackstead.invalid/0123456789ab/web-app:1.2');
    expect(parseLibraryRef(ref)).toEqual({ owner: '0123456789ab', slug: 'web-app', tag: '1.2' });
  });

  it.each([
    'nginx:1',
    'stackstead.invalid/0123456789ab/web',
    'stackstead.invalid/0123456789ab/Bad_Slug:1',
    'stackstead.invalid/short/web:1',
    'stackstead/0123456789ab/web:1',
  ])('rejects %s', (s) => {
    expect(parseLibraryRef(s)).toBeNull();
  });
});
