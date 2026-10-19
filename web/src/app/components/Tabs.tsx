import { useId, useRef, useState } from 'react';
import type { KeyboardEvent, ReactNode } from 'react';
import styles from './Tabs.module.css';

export interface TabsProps {
  tabs: { id: string; label: string; badge?: string | number }[];
  selected: string;
  /** Return false to refuse the change (focus then stays put in automatic mode). */
  onSelect: (id: string) => void | boolean;
  ariaLabel?: string;
  /** manual (default): arrows move focus, Enter/Space select. auto: arrows select too. */
  activation?: 'manual' | 'auto';
  children?: (selected: string) => ReactNode;
}

export function Tabs({ tabs, selected, onSelect, ariaLabel, activation = 'manual', children }: TabsProps) {
  const uid = useId();
  const buttons = useRef<Record<string, HTMLButtonElement | null>>({});
  const [focused, setFocused] = useState<string>();
  // Roving tabindex: the focused tab, else the selected one.
  const stop = tabs.some((tab) => tab.id === focused) ? focused : selected;

  function move(index: number) {
    const target = tabs[(index + tabs.length) % tabs.length];
    if (activation === 'auto' && onSelect(target.id) === false) return;
    setFocused(target.id);
    buttons.current[target.id]?.focus();
  }

  function handleKeyDown(event: KeyboardEvent, index: number) {
    const next: Record<string, number> = {
      ArrowRight: index + 1,
      ArrowLeft: index - 1,
      Home: 0,
      End: tabs.length - 1,
    };
    if (!(event.key in next)) return;
    event.preventDefault();
    move(next[event.key]);
  }

  return (
    <>
      {/* Forget the remembered tab on leaving the list so re-entry lands on the selected one. */}
      <div
        role="tablist"
        className={styles.tablist}
        aria-label={ariaLabel}
        onBlur={(event) => {
          if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setFocused(undefined);
        }}
      >
        {tabs.map((tab, index) => (
          <button
            key={tab.id}
            ref={(el) => {
              buttons.current[tab.id] = el;
            }}
            type="button"
            role="tab"
            className={styles.tab}
            aria-label={tab.badge === undefined ? undefined : `${tab.label}, ${tab.badge}`}
            id={`${uid}-tab-${tab.id}`}
            aria-selected={tab.id === selected}
            aria-controls={`${uid}-panel`}
            tabIndex={tab.id === stop ? 0 : -1}
            onFocus={() => setFocused(tab.id)}
            onClick={() => onSelect(tab.id)}
            onKeyDown={(event) => handleKeyDown(event, index)}
          >
            {tab.label}
            {tab.badge !== undefined && <span className={styles.badge}>{tab.badge}</span>}
          </button>
        ))}
      </div>
      <div role="tabpanel" className={styles.panel} id={`${uid}-panel`} aria-labelledby={`${uid}-tab-${selected}`} tabIndex={0}>
        {children?.(selected)}
      </div>
    </>
  );
}
