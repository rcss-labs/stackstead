import type { ReactNode } from 'react';
import styles from './Field.module.css';

/** What the caller spreads onto its control. */
export interface FieldControlProps {
  id: string;
  'aria-describedby'?: string;
  'aria-invalid'?: 'true';
  /**
   * True when the field is read-only. Text inputs/textareas take `readOnly={readOnly}`;
   * selects, checkboxes and radios ignore `readOnly`, so render them `disabled={readOnly}`
   * (the visible readonlyReason explains why).
   */
  readOnly: boolean;
}

export interface FieldProps {
  label: string;
  id: string;
  error?: string;
  hint?: string;
  readonly?: boolean;
  readonlyReason?: string;
  mono?: boolean;
  children: (control: FieldControlProps) => ReactNode;
}

export function Field({ label, id, error, hint, readonly = false, readonlyReason, mono = false, children }: FieldProps) {
  const errorId = `${id}-error`;
  const hintId = `${id}-hint`;
  const reasonId = `${id}-readonly`;
  const described = [error ? errorId : '', hint ? hintId : '', readonly && readonlyReason ? reasonId : ''].filter(Boolean);
  const control: FieldControlProps = {
    id,
    'aria-describedby': described.length ? described.join(' ') : undefined,
    'aria-invalid': error ? 'true' : undefined,
    readOnly: readonly,
  };

  return (
    <div className={styles.field} data-mono={mono ? 'true' : undefined}>
      <label htmlFor={id}>{label}</label>
      {children(control)}
      {hint && (
        <p id={hintId} className={styles.hint}>
          {hint}
        </p>
      )}
      {readonly && readonlyReason && (
        <p id={reasonId} className={styles.hint}>
          {readonlyReason}
        </p>
      )}
      {error && (
        <p id={errorId} className={styles.error}>
          <span aria-hidden="true">✖ </span>
          {error}
        </p>
      )}
    </div>
  );
}
