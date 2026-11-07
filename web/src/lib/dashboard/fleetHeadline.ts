// Kept apart from overview.ts: only the lazy All servers screen uses it, so it stays out of the eager entry.
import type { StackSummary } from '../api/types';
import { attentionCount } from '../stacks/summary';

const plural = (n: number, word: string) => `${n} ${word}${n === 1 ? '' : 's'}`;

/** The all-servers verdict. It speaks only about reachability and the rows it kept, never about a server that is not answering. */
export function fleetHeadline(servers: { status: string }[], stacks: StackSummary[]): { title: string; sub: string } {
  const reachable = servers.filter((s) => s.status === 'reachable').length;
  const title =
    reachable === servers.length
      ? servers.length === 1
        ? 'This server is reachable'
        : `All ${servers.length} servers are reachable`
      : `${reachable} of ${servers.length} servers are reachable`;
  const parts = [plural(stacks.length, 'stack')];
  const attention = attentionCount(stacks);
  if (attention > 0) parts.push(`${attention} need${attention === 1 ? 's' : ''} attention`);
  if (reachable < servers.length) parts.push('stacks on servers that are not answering are hidden');
  return { title, sub: parts.join(' · ') };
}
