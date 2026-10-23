import { describe, expect, it } from 'vitest';
import { operationSubject } from './ops';

describe('operationSubject', () => {
  it('names an image operation after its image', () => {
    expect(operationSubject({ stack: 'image/0123456789ab/web' })).toEqual({
      label: 'image web',
      href: '/images/web',
    });
  });

  it('names a stack operation after its stack', () => {
    expect(operationSubject({ stack: 'shop' })).toEqual({ label: 'shop', href: '/stacks/shop' });
  });
});
