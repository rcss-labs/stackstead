import { useEffect, useId, useRef, useState, type FocusEvent, type ReactNode } from 'react';
import type { Action, StackSummary } from '$lib/api/types';
import { FILE_PATH } from '$lib/shell/paths';
import { ICONS } from '$lib/shell/icons';
import { BUSY_REASON } from '$lib/stacks/lifecycle';
import { toasts } from '$lib/stores/toasts';
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
  /** Opens the compose file item of the page; the menu entry is hidden without it. */
  onViewCompose?: () => void;
}

const PRIMARY: Action[] = ['up', 'restart', 'pull'];
const label = (action: string) => action.charAt(0).toUpperCase() + action.slice(1);

const icon = (d: string) => (
  <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d={d} />
  </svg>
);
const HAMMER = 'M14 6l4 4M3 21l9-9M13 3l8 8-3 3-8-8z';
const TRASH = 'M4 7h16M10 11v6M14 11v6M6 7l1 12h10l1-12M9 7V4h6v3';

async function copyText(text: string) {
  const ok = await navigator.clipboard.writeText(text).then(() => true, () => false);
  toasts.push(ok ? { kind: 'success', message: 'Path copied' } : { kind: 'warning', message: 'Could not copy the path' });
}

export function StackActions({ stack, lifecycleDisabledReason, busy = false, onAct, onRemove, onViewCompose }: StackActionsProps) {
  const volumesId = useId();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [withVolumes, setWithVolumes] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const moreButton = useRef<HTMLButtonElement>(null);

  const reason = lifecycleDisabledReason || (busy ? BUSY_REASON : undefined);

  useEffect(() => {
    if (!menuOpen) return;
    const away = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setMenuOpen(false);
    };
    // Escape is handled on the document: after a mouse open, Safari and Firefox leave focus on the body.
    const escape = (event: globalThis.KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      event.stopPropagation();
      setMenuOpen(false);
      moreButton.current?.focus();
    };
    document.addEventListener('pointerdown', away);
    document.addEventListener('keydown', escape);
    return () => {
      document.removeEventListener('pointerdown', away);
      document.removeEventListener('keydown', escape);
    };
  }, [menuOpen]);

  function openConfirm() {
    setWithVolumes(false);
    setConfirmOpen(true);
  }
  function confirm() {
    setConfirmOpen(false);
    onRemove(withVolumes);
  }
  function closeMenu(refocus = false) {
    setMenuOpen(false);
    if (refocus) moreButton.current?.focus();
  }

  function onBlur(event: FocusEvent) {
    const next = event.relatedTarget as Node | null;
    if (menuOpen && next && !root.current?.contains(next)) setMenuOpen(false);
  }

  const item = (key: string, text: string, d: string, run: () => void, opts: { reason?: string; danger?: boolean } = {}) => (
    <button
      key={key}
      type="button"
      className={styles.item}
      data-danger={opts.danger || undefined}
      aria-disabled={opts.reason ? 'true' : undefined}
      title={opts.reason}
      onClick={() => {
        if (opts.reason) return;
        closeMenu(true); // the opener must exist again before a dialog opens, or Dialog finds nothing to refocus
        run();
      }}
    >
      {icon(d)}
      {text}
    </button>
  );
  const items: ReactNode[] = [
    item('build', 'Build images', HAMMER, () => onAct('build'), { reason }),
    ...(onViewCompose ? [item('compose', 'View compose file', FILE_PATH, onViewCompose)] : []),
    item('copy', 'Copy path', ICONS.copy, () => void copyText(stack.path)),
    <hr key="rule" className={styles.rule} />,
    item('down', 'Down', ICONS.stop, () => onAct('down'), { reason }),
    item('remove', 'Remove stack', TRASH, openConfirm, { reason: busy ? BUSY_REASON : undefined, danger: true }),
  ];

  return (
    <>
      <div ref={root} className={styles.cluster} role="group" aria-label={`Actions for ${stack.name}`} onBlur={onBlur}>
        {PRIMARY.map((action) => {
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
        <span className={styles.more}>
          <button
            ref={moreButton}
            type="button"
            className={styles.moreButton}
            aria-label="More actions"
            aria-expanded={menuOpen}
            onClick={() => setMenuOpen(!menuOpen)}
          >
            {icon(ICONS.more)}
          </button>
          {menuOpen && (
            <div role="group" aria-label="More actions" className={styles.menu}>
              {items}
            </div>
          )}
        </span>
      </div>
      <ConfirmDialog
        open={confirmOpen}
        title={`Remove ${stack.name}?`}
        message="This stops and removes the stack's containers and networks and unregisters it from Stackstead."
        confirmLabel="Remove"
        danger
        typedConfirm={withVolumes ? stack.name : undefined}
        onConfirm={confirm}
        onCancel={() => setConfirmOpen(false)}
      >
        <label className={styles.volumes} htmlFor={volumesId}>
          <input id={volumesId} type="checkbox" checked={withVolumes} onChange={(e) => setWithVolumes(e.target.checked)} />
          Also remove volumes
        </label>
        {withVolumes && <p className={styles.warn}>Deleted volumes cannot be recovered.</p>}
        <p className={styles.note}>Files on disk are never deleted.</p>
      </ConfirmDialog>
    </>
  );
}
