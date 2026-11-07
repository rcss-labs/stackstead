import type { Action, StackDetail, StackStatus } from '$lib/api/types';
import { CopyButton } from '../components/CopyButton';
import { Led, type LedProps } from '../components/Led';
import { ServiceCells } from '../components/ServiceCells';
import { StatusBadge } from '../components/StatusBadge';
import { serviceState } from '$lib/stacks/summary';
import styles from './DetailHeader.module.css';
import { StackActions } from './StackActions';

export interface DetailHeaderProps {
  detail: StackDetail;
  busy: boolean;
  lifecycleReason: string | undefined;
  onAct: (action: Action) => void;
  onRemove: (withVolumes: boolean) => void;
  onViewCompose?: () => void;
}

const LED: Record<StackStatus, { tone: LedProps['tone']; pulse: boolean }> = {
  running: { tone: 'success', pulse: false },
  partial: { tone: 'warning', pulse: true },
  stopped: { tone: 'faint', pulse: false },
  unavailable: { tone: 'danger', pulse: false },
  unknown: { tone: 'muted', pulse: false },
};

export function DetailHeader({ detail, busy, lifecycleReason, onAct, onRemove, onViewCompose }: DetailHeaderProps) {
  const led = LED[detail.status] ?? LED.unknown;
  return (
    <header className={styles.header}>
      <Led tone={led.tone} pulse={led.pulse} size={10} />
      <h1 className={styles.name}>{detail.name}</h1>
      <StatusBadge status={detail.status} />
      {detail.services.length > 0 && <HeaderCells detail={detail} />}
      <p className={styles.meta}>
        <span className={styles.prefix}>
          {detail.running}/{detail.serviceCount} up · {detail.origin} ·
        </span>{' '}
        <span className={styles.path} title={detail.path}>
          {detail.path}
        </span>
        <CopyButton text={detail.path} variant="icon" label="Copy path" />
      </p>
      <div className={styles.actions}>
        <StackActions stack={detail} lifecycleDisabledReason={lifecycleReason} busy={busy} onAct={onAct} onRemove={onRemove} onViewCompose={onViewCompose} />
      </div>
    </header>
  );
}

/** One cell per service, from the same states the rail shows; stopped services are the empty cells. */
function HeaderCells({ detail }: { detail: StackDetail }) {
  const tones = detail.services.map((service) => serviceState(service).tone);
  const count = (tone: string) => tones.filter((t) => t === tone).length;
  const good = count('success');
  const starting = count('warning');
  const bad = count('danger');
  const title = `${good} running, ${starting} starting, ${bad} failing`;
  return <ServiceCells count={tones.length} good={good} bad={bad} starting={starting} title={title} />;
}
