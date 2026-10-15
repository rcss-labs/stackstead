<script lang="ts">
  let { status }: { status: string } = $props();

  type Tone = 'success' | 'warning' | 'muted' | 'danger';
  const looks: Record<string, { tone: Tone; label: string }> = {
    running: { tone: 'success', label: 'running' },
    succeeded: { tone: 'success', label: 'succeeded' },
    healthy: { tone: 'success', label: 'healthy' },
    partial: { tone: 'warning', label: 'partial' },
    timed_out: { tone: 'warning', label: 'timed out' },
    starting: { tone: 'warning', label: 'starting' },
    restarting: { tone: 'warning', label: 'restarting' },
    stopped: { tone: 'muted', label: 'stopped' },
    cancelled: { tone: 'muted', label: 'cancelled' },
    unknown: { tone: 'muted', label: 'unknown' },
    exited: { tone: 'muted', label: 'exited' },
    created: { tone: 'muted', label: 'created' },
    paused: { tone: 'warning', label: 'paused' },
    dead: { tone: 'danger', label: 'dead' },
    none: { tone: 'muted', label: 'none' },
    unavailable: { tone: 'danger', label: 'unavailable' },
    failed: { tone: 'danger', label: 'failed' },
    unhealthy: { tone: 'danger', label: 'unhealthy' },
  };
  // Docker may report states we do not know (e.g. removing): show the raw word, muted.
  const look = $derived(looks[status] ?? { tone: 'muted' as Tone, label: status });
</script>

<span class="badge {look.tone}">{look.label}</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    height: 20px;
    padding: 0 7px;
    border-radius: 4px;
    font-size: 11px;
    font-weight: 600;
    letter-spacing: 0.02em;
    text-transform: uppercase;
    white-space: nowrap;
    background: var(--tone-soft);
    color: var(--tone);
  }
  .success { --tone: var(--success); --tone-soft: var(--success-soft); }
  .warning { --tone: var(--warning); --tone-soft: var(--warning-soft); }
  .danger { --tone: var(--danger); --tone-soft: var(--danger-soft); }
  .muted { --tone: var(--muted); --tone-soft: var(--muted-soft); }
</style>
