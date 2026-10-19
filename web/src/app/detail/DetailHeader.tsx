import type { Action, StackDetail, StackStatus } from '$lib/api/types';
import { CopyButton } from '../components/CopyButton';
import { Led, type LedProps } from '../components/Led';
import { StatusBadge } from '../components/StatusBadge';
import styles from './DetailHeader.module.css';
import { StackActions } from './StackActions';

export interface DetailHeaderProps {
  detail: StackDetail;
  busy: boolean;
  lifecycleReason: string | undefined;
  onAct: (action: Action) => void;
  onRemove: (withVolumes: boolean) => void;
}

const LED: Record<StackStatus, { tone: LedProps['tone']; pulse: boolean }> = {
  running: { tone: 'success', pulse: false },
  partial: { tone: 'warning', pulse: true },
  stopped: { tone: 'faint', pulse: false },
  unavailable: { tone: 'danger', pulse: false },
  unknown: { tone: 'muted', pulse: false },
};

export function DetailHeader({ detail, busy, lifecycleReason, onAct, onRemove }: DetailHeaderProps) {
  const led = LED[detail.status] ?? LED.unknown;
  return (
    <header className={styles.header}>
      <Led tone={led.tone} pulse={led.pulse} />
      <h1 className={styles.name}>{detail.name}</h1>
      <StatusBadge status={detail.status} />
      <p className={styles.meta}>
        {detail.running}/{detail.serviceCount} up · {detail.origin} · <span className={styles.path}>{detail.path}</span>
        <CopyButton text={detail.path} variant="icon" label="Copy path" />
      </p>
      <div className={styles.actions}>
        <StackActions stack={detail} lifecycleDisabledReason={lifecycleReason} busy={busy} onAct={onAct} onRemove={onRemove} />
      </div>
    </header>
  );
}
