import { Link } from 'react-router';
import type { ContainerRow } from '$lib/api/types';
import { ICONS } from '$lib/shell/icons';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import styles from './ContainerActions.module.css';
import type { ConfirmingAction, ContainerAction } from './useContainerAction';

const ICON_OF: Record<ContainerAction, 'play' | 'stop' | 'restart'> = { start: 'play', stop: 'stop', restart: 'restart' };
const labelOf = (action: ContainerAction) => action[0].toUpperCase() + action.slice(1);

export interface ContainerActionsProps {
  row: ContainerRow;
  /** `icon`: 26 px icon buttons (table rows); `md`: labelled buttons (page header). */
  size: 'icon' | 'md';
  /** Why the actions are unavailable right now, or undefined when they are allowed. */
  reason: string | undefined;
  onRequest: (action: ContainerAction) => void;
}

export function ContainerActions({ row, size, reason, onRequest }: ContainerActionsProps) {
  if (row.managed === 'stack') {
    return (
      <Link to={`/stacks/${encodeURIComponent(row.stack)}`} className={styles.manage}>
        Manage in stack →
      </Link>
    );
  }
  const actions: ContainerAction[] = row.state === 'running' ? ['stop', 'restart'] : ['start', 'restart'];
  const px = size === 'icon' ? 14 : 16;
  return (
    <span className={styles.btns}>
      {actions.map((action) => {
        const label = `${labelOf(action)} ${row.name}`;
        const outline = action === 'restart';
        return (
          <Button
            key={action}
            variant="ghost"
            size={size === 'icon' ? 'sm' : 'md'}
            icon={size === 'icon'}
            data-action={action}
            aria-label={label}
            title={label}
            disabledReason={reason}
            onClick={() => onRequest(action)}
          >
            <svg
              width={px}
              height={px}
              viewBox="0 0 24 24"
              fill={outline ? 'none' : 'currentColor'}
              stroke={outline ? 'currentColor' : 'none'}
              strokeWidth="1.8"
              strokeLinecap="round"
              strokeLinejoin="round"
              aria-hidden="true"
            >
              <path d={ICONS[ICON_OF[action]]} />
            </svg>
            {size === 'md' && labelOf(action)}
          </Button>
        );
      })}
    </span>
  );
}

export interface ContainerConfirmDialogProps {
  confirming: ConfirmingAction | undefined;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ContainerConfirmDialog({ confirming, onConfirm, onCancel }: ContainerConfirmDialogProps) {
  const shown = confirming;
  const name = shown?.row.name ?? '';
  const label = shown ? labelOf(shown.action) : '';
  const message =
    shown?.action === 'stop'
      ? `Stop container ${name}? Anything it serves goes offline until it is started again.`
      : `Restart container ${name}? It is briefly unavailable while it restarts.`;
  return <ConfirmDialog open={!!confirming} danger title={`${label} ${name}?`} message={message} confirmLabel={label} onConfirm={onConfirm} onCancel={onCancel} />;
}
