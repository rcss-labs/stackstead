import { describe, expect, it } from 'vitest';
import { changedLines } from './changedLines';

describe('changedLines', () => {
  it('returns nothing for identical text', () => {
    expect(changedLines('a: 1\nb: 2\n', 'a: 1\nb: 2\n')).toEqual([]);
  });

  it('marks a modified line', () => {
    expect(changedLines('a: 1\nb: 2\nc: 3\n', 'a: 1\nb: 9\nc: 3\n')).toEqual([2]);
  });

  it('marks every line of an inserted block', () => {
    expect(changedLines('a: 1\nd: 4\n', 'a: 1\nb: 2\nc: 3\nd: 4\n')).toEqual([2, 3]);
  });

  it('marks the neighbouring line where a line was deleted', () => {
    expect(changedLines('a: 1\nb: 2\nc: 3\n', 'a: 1\nc: 3\n')).toEqual([2]);
  });

  it('treats CRLF input like LF', () => {
    expect(changedLines('a: 1\r\nb: 2\r\n', 'a: 1\nb: 2\n')).toEqual([]);
    expect(changedLines('a: 1\r\nb: 2\r\n', 'a: 1\r\nb: 3\r\n')).toEqual([2]);
  });

  it('flags a trailing newline change on the last text line', () => {
    expect(changedLines('a: 1', 'a: 1\n')).toEqual([1]);
    expect(changedLines('a: 1\n', 'a: 1')).toEqual([1]);
  });
});
