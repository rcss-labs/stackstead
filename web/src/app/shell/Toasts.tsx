import { createPortal } from 'react-dom';
import { Link } from 'react-router';
import { useStore } from '../hooks/useStore';
import { Spinner } from '../components/Spinner';
import { toasts } from '$lib/stores/toasts';
import styles from './Toasts.module.css';

export interface ToastsProps {
  /** Follows a toast link; the app passes its unsaved-changes guard (the region is outside the guarded frame). */
  onNavigate?: (href: string) => void;
}

// Rendered under <body>, not #app: a modal Dialog makes #app inert, and toasts must stay reachable.
export function Toasts({ onNavigate }: ToastsProps) {
  const list = useStore(toasts);
  return createPortal(
    <section aria-label="Notifications" aria-live="polite" className={styles.region}>
      {list.map((toast) => (
        <div key={toast.id} className={styles.toast} data-kind={toast.kind} role={toast.kind === 'danger' ? 'alert' : undefined}>
          {toast.pending && <Spinner label="In progress" />}
          <span className={styles.message}>{toast.message}</span>
          {toast.link && (
            <Link
              className={styles.link}
              to={toast.link.href}
              onClick={(event) => {
                // Same exclusions as the frame's click guard: let the browser handle new-tab and download clicks.
                if (!onNavigate || event.defaultPrevented || event.button !== 0) return;
                if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
                const a = event.currentTarget;
                if ((a.target && a.target !== '_self') || a.hasAttribute('download')) return;
                event.preventDefault();
                onNavigate(toast.link!.href);
              }}
            >
              {toast.link.label}
            </Link>
          )}
          <button type="button" className={styles.close} aria-label="Dismiss notification" onClick={() => toasts.dismiss(toast.id)}>
            ×
          </button>
        </div>
      ))}
    </section>,
    document.body,
  );
}
