import { describe, expect, it } from 'vitest';
import { changedLineCount } from './changedLineCount';

describe('changedLineCount', () => {
  it('is zero for identical text, ignoring line endings and one trailing newline', () => {
    expect(changedLineCount('a\nb\n', 'a\r\nb')).toBe(0);
  });

  it('counts a modified line once', () => {
    expect(changedLineCount('a\nb\nc\n', 'a\nB\nc\n')).toBe(1);
  });

  it('counts inserted and deleted lines', () => {
    expect(changedLineCount('a\nc\n', 'a\nb\nc\n')).toBe(1);
    expect(changedLineCount('a\nb\nc\n', 'a\nc\n')).toBe(1);
    expect(changedLineCount('a\n', 'a\nx\ny\n')).toBe(2);
  });

  it('does not count lines that merely moved down', () => {
    expect(changedLineCount('x: 1\ny: 2\n', 'new: 0\nx: 1\ny: 2\n')).toBe(1);
  });

  it('stays bounded on very large unrelated texts', () => {
    const a = Array.from({ length: 5000 }, (_, i) => `a${i}`).join('\n');
    const b = Array.from({ length: 5000 }, (_, i) => `b${i}`).join('\n');
    expect(changedLineCount(a, b)).toBe(5000);
  });
});
