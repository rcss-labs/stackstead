import { useEffect, useId, useMemo, useRef, useState } from 'react';
import type { KeyboardEvent } from 'react';
import { useNavigate } from 'react-router';
import { api } from '$lib/api/client';
import type { Operation } from '$lib/api/types';
import { effectiveTheme, setTheme } from '$lib/stores/appearance';
import { live } from '$lib/stores/live';
import { trackOperation } from '$lib/stores/operationTracker';
import { refreshRunningOps, stacks } from '$lib/stores/stacks';
import { toasts } from '$lib/stores/toasts';
import { Dialog } from '../components/Dialog';
import { Led } from '../components/Led';
import { PanelLabel } from '../components/PanelLabel';
import { useStore } from '../hooks/useStore';
import styles from './CommandPalette.module.css';
import { nextIndex, paletteEntries } from './palette';
import type { PaletteEntry, PaletteGroup } from './palette';

export interface CommandPaletteProps {
  open: boolean;
  /** The app's running operations; a stack with one is busy. */
  runningOps: Operation[];
  onClose: () => void;
  /** Follows a navigate command; the app passes its unsaved-changes guard. Defaults to the router. */
  onNavigate?: (href: string) => void;
}

export default function CommandPalette({ open, runningOps, onClose, onNavigate }: CommandPaletteProps) {
  const uid = useId();
  const navigate = useNavigate();
  const { items } = useStore(stacks);
  const liveState = useStore(live);
  const theme = useStore(effectiveTheme);
  const [query, setQuery] = useState('');
  const [selected, setSelected] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);

  const busyStacks = useMemo(() => new Set(runningOps.map((op) => op.stack)), [runningOps]);
  const entries = useMemo(
    () => paletteEntries({ query, stacks: items, live: liveState, busyStacks }),
    [query, items, liveState, busyStacks],
  );
  const active = Math.min(selected, Math.max(0, entries.length - 1));
  const optionId = (index: number) => `${uid}-option-${index}`;

  // A reopened palette starts empty.
  useEffect(() => {
    if (!open) {
      setQuery('');
      setSelected(0);
    }
  }, [open]);

  useEffect(() => {
    document.getElementById(optionId(active))?.scrollIntoView?.({ block: 'nearest' });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, entries]);

  function run(entry: PaletteEntry | undefined) {
    if (!entry) return;
    const { command } = entry;
    if (command.kind === 'act') {
      if (entry.disabledReason) return; // the reason is already shown on the row
      onClose();
      void runAction(command.stack, command.action);
      return;
    }
    onClose();
    if (command.kind === 'navigate') (onNavigate ?? navigate)(command.to);
    else setTheme(theme === 'dark' ? 'light' : 'dark');
  }

  async function runAction(stack: string, action: Parameters<typeof api.act>[1]) {
    try {
      const op = await api.act(stack, action);
      trackOperation(op); // the tracker resolves the pending toast once the operation ends
      refreshRunningOps(); // the top-bar pill and busy state come from the app's running operations
    } catch (e) {
      toasts.push({ kind: 'danger', message: e instanceof Error ? e.message : String(e) });
    }
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      setSelected(nextIndex(active, event.key === 'ArrowDown' ? 1 : -1, entries.length));
    } else if (event.key === 'Enter') {
      event.preventDefault();
      run(entries[active]);
    }
  }

  const groups = (['Go to', 'Stacks', 'Actions'] as PaletteGroup[])
    .map((group) => ({ group, rows: entries.map((entry, index) => ({ entry, index })).filter((r) => r.entry.group === group) }))
    .filter((g) => g.rows.length > 0);

  const listId = `${uid}-listbox`;
  const titleId = `${uid}-title`;

  return (
    <Dialog open={open} title="Command palette" labelledBy={titleId} onClose={onClose} width="lg" placement="top" closeOnBackdrop>
      <h2 id={titleId} className={styles.visuallyHidden}>
        Command palette
      </h2>
      <div className={styles.palette}>
        <div className={styles.searchRow}>
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" aria-hidden="true">
            <path d="M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14zM20 20l-3.5-3.5" />
          </svg>
          <input
            className={styles.input}
            role="combobox"
            aria-label="Search or run a command"
            aria-expanded="true"
            aria-controls={listId}
            aria-activedescendant={entries.length > 0 ? optionId(active) : undefined}
            aria-autocomplete="list"
            autoComplete="off"
            spellCheck={false}
            placeholder="Jump to a stack, container or run an action…"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value);
              setSelected(0);
            }}
            onKeyDown={onKeyDown}
          />
          <kbd className={styles.kbd}>esc</kbd>
        </div>
        <div ref={listRef} id={listId} role="listbox" aria-label="Results" className={styles.results} onMouseDown={(event) => event.preventDefault()}>
          {groups.map(({ group, rows }) => (
            <div key={group} role="group" aria-labelledby={`${uid}-group-${group}`}>
              <div className={styles.groupLabel}>
                <PanelLabel tone="faint" id={`${uid}-group-${group}`}>
                  {group}
                </PanelLabel>
              </div>
              {rows.map(({ entry, index }) => (
                <div
                  key={entry.id}
                  id={optionId(index)}
                  role="option"
                  aria-selected={index === active}
                  aria-disabled={entry.disabledReason ? true : undefined}
                  className={styles.option}
                  onMouseEnter={() => setSelected(index)}
                  onClick={() => run(entry)}
                >
                  {entry.led ? <Led tone={entry.led.tone} pulse={entry.led.pulse} size={7} /> : <i className={styles.dot} aria-hidden="true" />}
                  <span className={styles.label} data-mono={entry.mono ? 'true' : undefined}>
                    {entry.label}
                  </span>
                  {entry.hint && <span className={styles.hint}>{entry.hint}</span>}
                  {index === active && !entry.disabledReason && (
                    <kbd className={styles.enter} aria-hidden="true">
                      ↵
                    </kbd>
                  )}
                </div>
              ))}
            </div>
          ))}
          {entries.length === 0 && <div className={styles.empty}>No matches</div>}
        </div>
        <div className={styles.footer} aria-hidden="true">
          <span>↑↓ move</span>
          <span>↵ run</span>
          <span>esc close</span>
        </div>
      </div>
    </Dialog>
  );
}
