import { useEffect, useRef, useState } from 'react';
import { flushSync } from 'react-dom';
import styles from './CopyButton.module.css';

export interface CopyButtonProps {
  text: string;
  label?: string;
  variant?: 'ghost' | 'icon';
  size?: 'xs' | 'sm';
}

export function CopyButton({ text, label = 'Copy', variant = 'ghost', size = 'xs' }: CopyButtonProps) {
  const [outcome, setOutcome] = useState<'idle' | 'copied' | 'failed'>('idle');
  const buttonRef = useRef<HTMLButtonElement>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);

  useEffect(() => () => clearTimeout(timer.current), []);

  function fallbackCopy(value: string): boolean {
    const area = document.createElement('textarea');
    area.value = value;
    area.setAttribute('readonly', '');
    area.style.position = 'fixed';
    area.style.opacity = '0';
    document.body.appendChild(area);
    area.select();
    try {
      return document.execCommand('copy');
    } finally {
      area.remove();
      buttonRef.current?.focus(); // select() moved focus to the textarea
    }
  }

  async function copy() {
    let ok = false;
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
        ok = true;
      }
    } catch {
      ok = false;
    }
    if (!ok) ok = fallbackCopy(text);
    // Clear first (committed synchronously) so a repeat copy changes the live region and is announced again.
    flushSync(() => setOutcome('idle'));
    setOutcome(ok ? 'copied' : 'failed');
    clearTimeout(timer.current);
    if (ok) timer.current = setTimeout(() => setOutcome('idle'), 2000);
  }

  const message = outcome === 'copied' ? 'Copied' : outcome === 'failed' ? 'Copy failed — select and copy manually' : '';

  return (
    <>
      <button
        ref={buttonRef}
        type="button"
        className={styles.copy}
        data-variant={variant}
        data-size={size}
        aria-label={variant === 'icon' ? label : undefined}
        title={variant === 'icon' ? label : undefined}
        onClick={copy}
      >
        {variant === 'icon' ? <span aria-hidden="true">⧉</span> : label}
      </button>
      <span role="status" className={outcome === 'failed' ? styles.bad : 'visually-hidden'}>
        {message}
      </span>
      {outcome === 'copied' && (
        <span className={styles.ok} aria-hidden="true">
          Copied
        </span>
      )}
    </>
  );
}
