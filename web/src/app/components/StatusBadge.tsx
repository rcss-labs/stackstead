import styles from './StatusBadge.module.css';

type Tone = 'success' | 'warning' | 'muted' | 'danger';

const looks: Record<string, { tone: Tone; label: string }> = {
  running: { tone: 'success', label: 'running' },
  succeeded: { tone: 'success', label: 'succeeded' },
  healthy: { tone: 'success', label: 'healthy' },
  partial: { tone: 'warning', label: 'partial' },
  timed_out: { tone: 'warning', label: 'timed out' },
  starting: { tone: 'warning', label: 'starting' },
  restarting: { tone: 'warning', label: 'restarting' },
  paused: { tone: 'warning', label: 'paused' },
  stopped: { tone: 'muted', label: 'stopped' },
  cancelled: { tone: 'muted', label: 'cancelled' },
  unknown: { tone: 'muted', label: 'unknown' },
  exited: { tone: 'muted', label: 'exited' },
  created: { tone: 'muted', label: 'created' },
  none: { tone: 'muted', label: 'none' },
  unavailable: { tone: 'danger', label: 'unavailable' },
  failed: { tone: 'danger', label: 'failed' },
  unhealthy: { tone: 'danger', label: 'unhealthy' },
  dead: { tone: 'danger', label: 'dead' },
};

export function StatusBadge({ status }: { status: string }) {
  // Docker may report states we do not know (e.g. removing): show the raw word, muted.
  const look = Object.hasOwn(looks, status) ? looks[status] : { tone: 'muted' as Tone, label: status };
  return (
    <span className={styles.badge} data-tone={look.tone}>
      {look.label}
    </span>
  );
}
