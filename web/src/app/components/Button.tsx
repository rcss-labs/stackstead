import { useId } from 'react';
import type { ButtonHTMLAttributes, MouseEvent, ReactNode } from 'react';
import styles from './Button.module.css';

export type ButtonProps = {
  variant: 'primary' | 'secondary' | 'danger' | 'ghost';
  size?: 'md' | 'sm' | 'xs';
  icon?: boolean;
  /** Filled danger, used by confirmation dialogs so the destructive action is visible. */
  confirm?: boolean;
  loading?: boolean;
  disabled?: boolean;
  disabledReason?: string;
  type?: 'button' | 'submit' | 'reset';
  onClick?: (event: MouseEvent<HTMLButtonElement>) => void;
  children?: ReactNode;
} & Omit<ButtonHTMLAttributes<HTMLButtonElement>, 'type' | 'onClick'>;

export function Button({
  variant,
  size = 'md',
  icon = false,
  confirm = false,
  loading = false,
  disabled = false,
  disabledReason,
  type = 'button',
  onClick,
  children,
  title,
  className,
  ...rest
}: ButtonProps) {
  const uid = useId();
  const reasonId = `${uid}-reason`;
  // A disabledReason (or loading) soft-disables: aria-disabled + click ignored, but still
  // focusable so the reason can be found. Without a reason, `disabled` is a real disabled button.
  const soft = Boolean(disabledReason) || loading;

  function handleClick(event: MouseEvent<HTMLButtonElement>) {
    if (soft) {
      event.preventDefault();
      return;
    }
    onClick?.(event);
  }

  return (
    <>
      <button
        {...rest}
        className={className ? `${styles.btn} ${className}` : styles.btn}
        data-variant={variant}
        data-size={size}
        data-icon={icon ? 'true' : undefined}
        data-confirm={confirm ? 'true' : undefined}
        type={type}
        disabled={disabled && !disabledReason}
        aria-disabled={soft ? 'true' : undefined}
        aria-busy={loading ? 'true' : undefined}
        aria-describedby={disabledReason ? reasonId : rest['aria-describedby']}
        title={disabledReason ?? title}
        onClick={handleClick}
      >
        {loading && <span className={styles.spin} aria-hidden="true" />}
        {children}
      </button>
      {disabledReason && (
        <span id={reasonId} className="visually-hidden">
          {disabledReason}
        </span>
      )}
    </>
  );
}
