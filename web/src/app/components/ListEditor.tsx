import { useId, useLayoutEffect, useRef } from 'react';
import type { ReactNode } from 'react';
import styles from './ListEditor.module.css';

export interface ListEditorProps<T> {
  items: T[];
  onChange: (items: T[]) => void;
  addLabel: string;
  /** Value appended by the add button. */
  newItem: () => T;
  row: (item: T, index: number, update: (value: T) => void) => ReactNode;
  readonly?: boolean;
  errors?: Record<number, string>;
  /** Group heading shown left of the add button. */
  title?: string;
  hint?: string;
}

type PendingFocus = { row: number } | 'add' | null;

const FIELD = 'input, select, textarea, button:not([data-remove])';

export function ListEditor<T>({ items, onChange, addLabel, newItem, row, readonly = false, errors = {}, title, hint }: ListEditorProps<T>) {
  const uid = useId();
  const listRef = useRef<HTMLUListElement>(null);
  const addButtonRef = useRef<HTMLButtonElement>(null);
  // Where focus goes once the parent has applied the change.
  const pendingFocus = useRef<PendingFocus>(null);

  useLayoutEffect(() => {
    const target = pendingFocus.current;
    if (!target) return;
    pendingFocus.current = null;
    if (target === 'add') {
      addButtonRef.current?.focus();
      return;
    }
    listRef.current?.querySelectorAll<HTMLElement>(':scope > li')[target.row]?.querySelector<HTMLElement>(FIELD)?.focus();
  }, [items.length]);

  // If the parent ignored the change the length never moves; drop the request once the
  // render for this event has had its chance, so it cannot steal focus from a later update.
  // The clear relies on React flushing a synchronous parent update first; parents that update
  // in a transition or asynchronously get no focus move (same as the Svelte version).
  function change(next: T[], focus: PendingFocus) {
    pendingFocus.current = focus;
    onChange(next);
    queueMicrotask(() => {
      pendingFocus.current = null;
    });
  }

  function add() {
    change([...items, newItem()], { row: items.length });
  }

  function remove(index: number) {
    const remaining = items.length - 1;
    change(
      items.filter((_, i) => i !== index),
      remaining === 0 ? 'add' : { row: Math.min(Math.max(index - 1, 0), remaining - 1) },
    );
  }

  function update(index: number, value: T) {
    onChange(items.map((item, i) => (i === index ? value : item)));
  }

  return (
    <div className={styles.editor}>
      {(title || !readonly) && (
        <div className={styles.head}>
          {title && <span className={styles.title}>{title}</span>}
          {hint && <span className={styles.hint}>{hint}</span>}
          {!readonly && (
            <button type="button" className={styles.add} aria-label={addLabel} ref={addButtonRef} onClick={add}>
              + Add
            </button>
          )}
        </div>
      )}
      <ul ref={listRef} className={styles.rows}>
        {items.map((item, i) => (
          // Rows are positional: there is no stable identity beyond the index.
          <li key={i}>
            <div className={styles.row} role="group" aria-label={`Item ${i + 1}`} aria-describedby={errors[i] ? `${uid}-err-${i}` : undefined}>
              <div className={styles.cell}>{row(item, i, (value: T) => update(i, value))}</div>
              {!readonly && (
                <button type="button" className={styles.remove} data-remove aria-label={`Remove item ${i + 1}`} onClick={() => remove(i)}>
                  ×
                </button>
              )}
            </div>
            {errors[i] && (
              <p id={`${uid}-err-${i}`} className={styles.error}>
                <span aria-hidden="true">✖ </span>
                {errors[i]}
              </p>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
