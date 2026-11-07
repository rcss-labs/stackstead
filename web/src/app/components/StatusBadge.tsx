import type { ReactNode } from 'react';
import styles from './StatusBadge.module.css';

type Tone = 'success' | 'warning' | 'muted' | 'danger';
type Shape = 'ok' | 'partial' | 'alert' | 'failed' | 'off' | 'pending' | 'paused';

// Every status carries a shape as well as a colour, so it reads in greyscale and for colour-blind users.
const shapes: Record<Shape, ReactNode> = {
  ok: <circle cx="5" cy="5" r="3.5" fill="currentColor" />,
  partial: (
    <>
      <circle cx="5" cy="5" r="3.5" fill="none" stroke="currentColor" strokeWidth="1.5" />
      <path d="M5 1.5a3.5 3.5 0 0 1 0 7Z" fill="currentColor" />
    </>
  ),
  alert: <path d="M5 1 9.5 9h-9L5 1Z" fill="currentColor" />,
  failed: <path d="M2 2l6 6M8 2 2 8" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />,
  off: <rect x="2" y="2" width="6" height="6" rx="1" fill="none" stroke="currentColor" strokeWidth="1.5" />,
  pending: <circle cx="5" cy="5" r="3.5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeDasharray="2 2" />,
  paused: <path d="M3 2v6M7 2v6" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" />,
};

const looks: Record<string, { tone: Tone; label: string; shape: Shape }> = {
  running: { tone: 'success', label: 'running', shape: 'ok' },
  succeeded: { tone: 'success', label: 'succeeded', shape: 'ok' },
  healthy: { tone: 'success', label: 'healthy', shape: 'ok' },
  partial: { tone: 'warning', label: 'partial', shape: 'partial' },
  timed_out: { tone: 'warning', label: 'timed out', shape: 'partial' },
  starting: { tone: 'warning', label: 'starting', shape: 'pending' },
  restarting: { tone: 'warning', label: 'restarting', shape: 'alert' },
  paused: { tone: 'warning', label: 'paused', shape: 'paused' },
  stopped: { tone: 'muted', label: 'stopped', shape: 'off' },
  cancelled: { tone: 'muted', label: 'cancelled', shape: 'off' },
  unknown: { tone: 'muted', label: 'unknown', shape: 'off' },
  exited: { tone: 'muted', label: 'exited', shape: 'off' },
  created: { tone: 'muted', label: 'created', shape: 'off' },
  none: { tone: 'muted', label: 'none', shape: 'off' },
  unavailable: { tone: 'danger', label: 'unavailable', shape: 'failed' },
  failed: { tone: 'danger', label: 'failed', shape: 'failed' },
  unhealthy: { tone: 'danger', label: 'unhealthy', shape: 'alert' },
  dead: { tone: 'danger', label: 'dead', shape: 'failed' },
};

export function StatusBadge({ status }: { status: string }) {
  // Docker may report states we do not know (e.g. removing): show the raw word, muted.
  const look = Object.hasOwn(looks, status) ? looks[status] : { tone: 'muted' as Tone, label: status, shape: 'off' as Shape };
  return (
    <span className={styles.badge} data-tone={look.tone}>
      <svg className={styles.shape} data-shape={look.shape} width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
        {shapes[look.shape]}
      </svg>
      {look.label}
    </span>
  );
}
