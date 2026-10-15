// Imported only from the lazy editor chunk (cm.ts): it pulls in @codemirror/merge.
import { diff } from '@codemirror/merge';

const toLf = (text: string): string => text.replace(/\r\n/g, '\n');

/** 1-based line numbers in `draft` that differ from `original` (inserted or modified lines). */
export function changedLines(original: string, draft: string): number[] {
  const a = toLf(original);
  const b = toLf(draft);
  if (a === b) return [];

  const lineStarts = [0];
  for (let i = 0; i < b.length; i++) if (b[i] === '\n') lineStarts.push(i + 1);
  /** 1-based line containing offset `pos`. */
  const lineAt = (pos: number): number => {
    let lo = 0;
    let hi = lineStarts.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (lineStarts[mid] <= pos) lo = mid;
      else hi = mid - 1;
    }
    return lo + 1;
  };

  const lines = new Set<number>();
  for (const change of diff(a, b)) {
    // A deletion has an empty range in the draft: mark the line where it happened.
    const first = lineAt(change.fromB);
    const last = change.toB > change.fromB ? lineAt(change.toB - 1) : first;
    for (let n = first; n <= last; n++) lines.add(n);
  }
  return [...lines].sort((x, y) => x - y);
}
