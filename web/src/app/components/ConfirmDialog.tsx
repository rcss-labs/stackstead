import { useId } from 'react';
import type { ReactNode } from 'react';
import { Button } from './Button';
import styles from './ConfirmDialog.module.css';
import { Dialog } from './Dialog';

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
  children,
}: ConfirmDialogProps) {
  const messageId = `${useId()}-message`;
  return (
    <Dialog open={open} title={title} onClose={onCancel} role="alertdialog" describedBy={messageId}>
      <p id={messageId} className={styles.message}>
        {message}
      </p>
      {children}
      <div className={styles.actions}>
        {/* Cancel comes first so initial focus never lands on the destructive action. */}
        <Button variant="secondary" onClick={onCancel}>
          Cancel
        </Button>
        <Button
          variant={danger ? 'danger' : 'primary'}
          confirm={danger}
          disabledReason={confirmDisabledReason}
          loading={pending}
          onClick={onConfirm}
        >
          {confirmLabel}
        </Button>
      </div>
    </Dialog>
  );
}
