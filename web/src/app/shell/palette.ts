import type { Action, StackSummary } from '$lib/api/types';
import type { EffectiveTheme } from '$lib/stores/appearance';
import type { LiveState } from '$lib/stores/live';
import { lifecycleDisabledReason } from '$lib/stacks/lifecycle';
import { needsAttention } from '$lib/stacks/summary';
import type { LedProps } from '../components/Led';
import { NAV } from './nav';

export type PaletteGroup = 'Go to' | 'Stacks' | 'Actions';

export type PaletteCommand = { kind: 'navigate'; to: string } | { kind: 'act'; stack: string; action: Action } | { kind: 'theme' };

export interface PaletteEntry {
  id: string;
  group: PaletteGroup;
  label: string;
  hint?: string;
  mono?: boolean;
  led?: { tone: LedProps['tone']; pulse?: boolean };
  disabledReason?: string;
  command: PaletteCommand;
}

export interface PaletteInput {
  query: string;
  stacks: StackSummary[];
  live: Pick<LiveState, 'compose' | 'docker' | 'events'>;
  busyStacks: Set<string>;
  effectiveTheme: EffectiveTheme;
}

const GROUP_ORDER: PaletteGroup[] = ['Go to', 'Stacks', 'Actions'];

function stackLed(stack: StackSummary): NonNullable<PaletteEntry['led']> {
  switch (stack.status) {
    case 'running':
      return { tone: 'success' };
    case 'partial':
      return needsAttention([stack]) ? { tone: 'warning', pulse: true } : { tone: 'warning' };
    case 'unavailable':
      return { tone: 'danger' };
    default:
      return { tone: 'faint' };
  }
}

function lifecycleActions(stack: StackSummary, reason: string | undefined): PaletteEntry[] {
  const actions: Array<{ action: Action; verb: string; allowed: boolean }> = [
    { action: 'restart', verb: 'Restart', allowed: stack.status === 'running' || stack.status === 'partial' },
    { action: 'up', verb: 'Up', allowed: stack.status === 'stopped' || stack.status === 'partial' },
    { action: 'pull', verb: 'Pull', allowed: true },
  ];
  return actions
    .filter((a) => a.allowed)
    .map((a) => ({
      id: `act:${a.action}:${stack.name}`,
      group: 'Actions' as const,
      label: `${a.verb} ${stack.name}`,
      hint: reason,
      disabledReason: reason,
      command: { kind: 'act' as const, stack: stack.name, action: a.action },
    }));
}

/** Every palette entry for the current stores, filtered by the query, in group order. */
export function paletteEntries({ query, stacks, live, busyStacks }: PaletteInput): PaletteEntry[] {
  const pages: PaletteEntry[] = NAV.map((n) => ({
    id: `go:${n.href}`,
    group: 'Go to',
    label: n.label,
    hint: 'Page',
    command: { kind: 'navigate', to: n.href },
  }));

  const stackEntries: PaletteEntry[] = stacks.map((s) => ({
    id: `stack:${s.name}`,
    group: 'Stacks',
    label: s.name,
    hint: `${s.running}/${s.serviceCount} · ${s.status}`,
    mono: true,
    led: stackLed(s),
    command: { kind: 'navigate', to: `/stacks/${encodeURIComponent(s.name)}` },
  }));

  const q = query.trim().toLowerCase();
  const actions: PaletteEntry[] = [
    { id: 'act:new-stack', group: 'Actions', label: 'New stack', command: { kind: 'navigate', to: '/stacks/new' } },
    { id: 'act:cleanup', group: 'Actions', label: 'Clean up storage', command: { kind: 'navigate', to: '/storage' } },
    { id: 'act:theme', group: 'Actions', label: 'Toggle theme', hint: 'appearance', command: { kind: 'theme' } },
  ];
  if (q) {
    for (const s of stacks) {
      const reason = lifecycleDisabledReason({ live, status: s.status, busy: busyStacks.has(s.name) });
      actions.push(...lifecycleActions(s, reason));
    }
  }

  const all = [...pages, ...stackEntries, ...actions];
  const matching = q ? all.filter((e) => `${e.label} ${e.group} ${e.hint ?? ''}`.toLowerCase().includes(q)) : all;
  return GROUP_ORDER.flatMap((group) => matching.filter((e) => e.group === group));
}

/** Moves the highlight by one, stopping at the first and last entry (the design does not wrap). */
export function nextIndex(current: number, delta: 1 | -1, length: number): number {
  if (length === 0) return 0;
  return Math.min(length - 1, Math.max(0, current + delta));
}
