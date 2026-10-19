import { useEffect, useId, useLayoutEffect, useRef } from 'react';
import type { KeyboardEvent as ReactKeyboardEvent, ReactNode } from 'react';
import { createPortal } from 'react-dom';
import styles from './Dialog.module.css';

// Open dialogs, oldest first. Escape closes only the last one.
const openStack: symbol[] = [];

// Buttons clicked with a mouse do not take focus in Safari or Firefox on macOS, so the last
// click target stands in as the opener when nothing is focused.
let lastClick: { el: HTMLElement; at: number } | undefined;
if (typeof document !== 'undefined') {
  document.addEventListener(
    'click',
    (event) => {
      const el = event.target instanceof Element ? event.target.closest<HTMLElement>('button, a[href], [role="button"]') : null;
      lastClick = el ? { el, at: Date.now() } : undefined;
    },
    true,
  );
}

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

export interface DialogProps {
  open: boolean;
  title: string;
  onClose: () => void;
  children?: ReactNode;
  labelledBy?: string;
  describedBy?: string;
  role?: 'dialog' | 'alertdialog';
  width?: 'md' | 'lg';
}

export function Dialog({ open, title, onClose, children, labelledBy, describedBy, role = 'dialog', width = 'md' }: DialogProps) {
  const uid = useId();
  const titleId = labelledBy ?? `${uid}-title`;
  const panelRef = useRef<HTMLDivElement>(null);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  function focusables(): HTMLElement[] {
    const panel = panelRef.current;
    if (!panel) return [];
    return Array.from(panel.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((el) => {
      const style = getComputedStyle(el);
      return !el.closest('[hidden], [inert]') && style.display !== 'none' && style.visibility !== 'hidden';
    });
  }

  // Focus in on open; inert app root; give focus back to the opener on close.
  useLayoutEffect(() => {
    const panel = panelRef.current;
    if (!open || !panel) return;
    const active = document.activeElement instanceof HTMLElement && document.activeElement !== document.body ? document.activeElement : null;
    const opener = active ?? (lastClick && Date.now() - lastClick.at < 1000 && lastClick.el.isConnected ? lastClick.el : null);
    const appRoot = document.getElementById('app');
    const wasInert = appRoot?.inert ?? false;
    if (appRoot) appRoot.inert = true;
    (focusables()[0] ?? panel).focus();
    panel.scrollTop = 0;
    return () => {
      if (appRoot) appRoot.inert = wasInert;
      opener?.focus();
    };
  }, [open]);

  // Escape is handled on the document so it works wherever focus happens to be.
  useEffect(() => {
    if (!open) return;
    const me = Symbol('dialog');
    openStack.push(me);
    const onEscape = (event: KeyboardEvent) => {
      if (event.key !== 'Escape' || event.defaultPrevented) return;
      if (openStack[openStack.length - 1] !== me) return; // only the topmost dialog reacts
      event.preventDefault();
      onCloseRef.current();
    };
    document.addEventListener('keydown', onEscape);
    return () => {
      document.removeEventListener('keydown', onEscape);
      openStack.splice(openStack.indexOf(me), 1);
    };
  }, [open]);

  function handleKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    if (event.key !== 'Tab') return;
    const panel = panelRef.current;
    const items = focusables();
    if (items.length === 0) {
      event.preventDefault();
      panel?.focus();
      return;
    }
    const first = items[0];
    const last = items[items.length - 1];
    const active = document.activeElement;
    if (event.shiftKey && (active === first || active === panel)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && active === last) {
      event.preventDefault();
      first.focus();
    } else if (!panel?.contains(active)) {
      event.preventDefault();
      first.focus();
    }
  }

  if (!open) return null;

  // Rendered under <body> so the app root can be made inert.
  return createPortal(
    <div className={styles.backdrop}>
      <div
        ref={panelRef}
        className={styles.dialog}
        data-width={width}
        role={role}
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={describedBy}
        tabIndex={-1}
        onKeyDown={handleKeyDown}
      >
        {!labelledBy && <h2 id={titleId}>{title}</h2>}
        {children}
      </div>
    </div>,
    document.body,
  );
}
