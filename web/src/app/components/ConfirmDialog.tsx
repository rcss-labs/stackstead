import { useEffect, useId, useState } from 'react';
import type { ReactNode } from 'react';
import { Button } from './Button';
import styles from './ConfirmDialog.module.css';
import { Dialog } from './Dialog';
import { Field } from './Field';

export interface ConfirmDialogProps {
  open: boolean;
  title: string;
  message: string;
  confirmLabel: string;
  danger?: boolean;
  confirmDisabledReason?: string;
  pending?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
  /** Confirm stays disabled until this exact text is typed (e.g. the stack name before removing it). */
  typedConfirm?: string;
  /** Extra content between the message and the actions (e.g. an option checkbox). */
  children?: ReactNode;
}

export function ConfirmDialog({
  open,
  title,
  message,
  confirmLabel,
  danger = false,
  confirmDisabledReason,
  pending = false,
  onConfirm,
  onCancel,
  typedConfirm,
  children,
}: ConfirmDialogProps) {
  const uid = useId();
  const messageId = `${uid}-message`;
  const [typed, setTyped] = useState('');
  // A reopened dialog never remembers an earlier confirmation.
  useEffect(() => {
    if (!open) setTyped('');
  }, [open]);
  const typedPending = typedConfirm !== undefined && typed !== typedConfirm;
  const disabledReason = confirmDisabledReason ?? (typedPending ? `Type ${typedConfirm} to confirm` : undefined);
  return (
    <Dialog open={open} title={title} onClose={onCancel} role="alertdialog" describedBy={messageId}>
      <p id={messageId} className={styles.message}>
        {message}
      </p>
      {typedConfirm !== undefined && (
        <Field label={`Type ${typedConfirm} to confirm`} id={`${uid}-typed`} mono>
          {(control) => (
            <input {...control} value={typed} autoComplete="off" autoCapitalize="none" autoCorrect="off" spellCheck={false} onChange={(event) => setTyped(event.target.value)} />
          )}
        </Field>
      )}
      {children}
      <div className={styles.actions}>
        {/* Cancel comes first so initial focus never lands on the destructive action. */}
        <Button variant="secondary" onClick={onCancel}>
          Cancel
        </Button>
        <Button
          variant={danger ? 'danger' : 'primary'}
          confirm={danger}
          disabledReason={disabledReason}
          loading={pending}
          onClick={onConfirm}
        >
          {confirmLabel}
        </Button>
      </div>
    </Dialog>
  );
}
