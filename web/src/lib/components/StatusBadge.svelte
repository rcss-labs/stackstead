<script lang="ts">
  import type { OpState, StackStatus } from '$lib/api/types';

  let { status }: { status: StackStatus | OpState } = $props();

  const looks: Record<StackStatus | OpState, { icon: string; tone: string; label: string }> = {
    running: { icon: '●', tone: 'success', label: 'running' },
    partial: { icon: '◐', tone: 'warning', label: 'partial' },
    stopped: { icon: '■', tone: 'muted', label: 'stopped' },
    unavailable: { icon: '⚠', tone: 'danger', label: 'unavailable' },
    unknown: { icon: '?', tone: 'muted', label: 'unknown' },
    succeeded: { icon: '✔', tone: 'success', label: 'succeeded' },
    failed: { icon: '✖', tone: 'danger', label: 'failed' },
    cancelled: { icon: '⊘', tone: 'muted', label: 'cancelled' },
    timed_out: { icon: '⏱', tone: 'warning', label: 'timed out' },
  };
  const look = $derived(looks[status] ?? looks.unknown);
</script>

<span class="badge {look.tone}"><span aria-hidden="true">{look.icon}</span><span>{look.label}</span></span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    gap: var(--space-1);
    padding: 0 var(--space-2);
    border: 1px solid var(--tone);
    border-radius: 999px;
    color: var(--tone);
    font-size: 0.875rem;
    white-space: nowrap;
  }
  .success { --tone: var(--success); }
  .warning { --tone: var(--warning); }
  .danger { --tone: var(--danger); }
  .muted { --tone: var(--muted); }
</style>
