// Imported only from the lazy editor chunk (cm.ts): it pulls in @codemirror/merge.
import { diff } from '@codemirror/merge';

/** Lines without terminators; one trailing newline does not add an empty last line. */
function splitLines(text: string): string[] {
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  if (lines.length > 1 && lines[lines.length - 1] === '') lines.pop();
  return lines;
}

/**
 * 1-based line numbers in `draft` that differ from `original` (inserted or modified lines).
 *
 * diff() works on characters, so a line-level diff encodes each distinct line as one
 * character first; otherwise lines sharing a prefix get marked along with the real change.
 */
export function changedLines(original: string, draft: string): number[] {
  const a = splitLines(original);
  const b = splitLines(draft);

  const codes = new Map<string, string>();
  const encode = (lines: string[]): string =>
    lines
      .map((line) => {
        let ch = codes.get(line);
        if (ch === undefined) {
          ch = String.fromCharCode(0x100 + codes.size);
          codes.set(line, ch);
        }
        return ch;
      })
      .join('');

  const lines = new Set<number>();
  for (const change of diff(encode(a), encode(b))) {
    // Offsets are now line indices. A deletion has an empty range in the draft:
    // mark the line where it happened.
    const first = Math.min(change.fromB + 1, b.length);
    const last = change.toB > change.fromB ? change.toB : first;
    for (let n = first; n <= last; n++) lines.add(n);
  }
  // Only the trailing newline differs: flag the last text line.
  if (lines.size === 0 && original.replace(/\r\n/g, '\n') !== draft.replace(/\r\n/g, '\n')) {
    lines.add(Math.max(1, b.length));
  }
  return [...lines].sort((x, y) => x - y);
}
