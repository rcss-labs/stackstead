import { describe, expect, it } from 'vitest';
import { retentionText } from './retention';

describe('retentionText', () => {
  it('says keep forever when neither rule is set', () => {
    expect(retentionText({ keepLast: 0, keepDays: 0 })).toBe('Keep forever');
  });

  it('describes the count rule alone', () => {
    expect(retentionText({ keepLast: 5, keepDays: 0 })).toBe('Keep the last 5');
  });

  it('describes the age rule alone', () => {
    expect(retentionText({ keepLast: 0, keepDays: 30 })).toBe('Keep 30 days');
  });

  it('combines both rules', () => {
    expect(retentionText({ keepLast: 5, keepDays: 30 })).toBe(
      'Keep the last 5 and anything from the last 30 days',
    );
  });
});
