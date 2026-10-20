import { useId, useState } from 'react';
import { ACTIONS, type Action, type StackSummary } from '$lib/api/types';
import { ICONS } from '$lib/shell/icons';
import { BUSY_REASON } from '$lib/stacks/lifecycle';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import styles from './StackActions.module.css';

export interface StackActionsProps {
  stack: StackSummary;
  /** Why lifecycle actions are unavailable (compose missing, Docker down, parse error). */
  lifecycleDisabledReason?: string;
  /** An operation of this stack is running or an action request is in flight. */
  busy?: boolean;
  onAct: (action: Action) => void;
  onRemove: (withVolumes: boolean) => void;
}

const label = (action: string) => action.charAt(0).toUpperCase() + action.slice(1);

export function StackActions({ stack, lifecycleDisabledReason, busy = false, onAct, onRemove }: StackActionsProps) {
  const volumesId = useId();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [withVolumes, setWithVolumes] = useState(false);

  const reason = lifecycleDisabledReason || (busy ? BUSY_REASON : undefined);

  function openConfirm() {
    setWithVolumes(false);
    setConfirmOpen(true);
  }
  function confirm() {
    setConfirmOpen(false);
    onRemove(withVolumes);
  }

  return (
    <>
      <div className={styles.cluster} role="group" aria-label={`Actions for ${stack.name}`}>
        {ACTIONS.map((action) => {
          const primary = action === 'up' && stack.status !== 'running';
          return (
            <Button key={action} variant={primary ? 'primary' : 'secondary'} disabledReason={reason} onClick={() => onAct(action)}>
              {action === 'up' && (
                <svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
                  <path d={ICONS.play} />
                </svg>
              )}
              {label(action)}
            </Button>
          );
        })}
        <Button variant="danger" disabledReason={busy ? BUSY_REASON : undefined} onClick={openConfirm}>
          Remove
        </Button>
      </div>
      <ConfirmDialog
        open={confirmOpen}
        title={`Remove ${stack.name}?`}
        message="This stops and removes the stack's containers and networks and unregisters it from Stackstead."
        confirmLabel="Remove"
        danger
        onConfirm={confirm}
        onCancel={() => setConfirmOpen(false)}
      >
        <label className={styles.volumes} htmlFor={volumesId}>
          <input id={volumesId} type="checkbox" checked={withVolumes} onChange={(e) => setWithVolumes(e.target.checked)} />
          Also remove volumes
        </label>
        <p className={styles.note}>Files on disk are never deleted.</p>
      </ConfirmDialog>
    </>
  );
}
