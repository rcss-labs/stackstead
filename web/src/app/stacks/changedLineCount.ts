// Past this many line pairs the exact match is skipped; compose files are far smaller.
const MAX_PAIRS = 4_000_000;

function lines(text: string): string[] {
  const out = text.replace(/\r\n/g, '\n').split('\n');
  if (out.length > 1 && out[out.length - 1] === '') out.pop();
  return out;
}

/**
 * How many lines a change touches: the larger of the lines added and the lines removed, so one edited line
 * counts once. Plain text only, so the review step does not have to load the diff engine to state it.
 */
export function changedLineCount(original: string, modified: string): number {
  const a = lines(original);
  const b = lines(modified);
  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }
  const n = endA - start;
  const m = endB - start;
  if (n === 0 || m === 0 || n * m > MAX_PAIRS) return Math.max(n, m);

  // Longest common subsequence of the differing middle, one row at a time.
  let previous = new Array<number>(m + 1).fill(0);
  for (let i = 1; i <= n; i++) {
    const current = new Array<number>(m + 1).fill(0);
    for (let j = 1; j <= m; j++) {
      current[j] = a[start + i - 1] === b[start + j - 1] ? previous[j - 1] + 1 : Math.max(previous[j], current[j - 1]);
    }
    previous = current;
  }
  const common = previous[m];
  return Math.max(n - common, m - common);
}
