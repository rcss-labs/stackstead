import { useEffect, useRef } from 'react';

export interface ContainerKeysOptions {
  enabled: boolean;
  count: number;
  focusIndex: number;
  onMove: (index: number) => void;
  onToggle: () => void;
  onRestart: () => void;
  onClose: () => void;
}

/** True when the key belongs to the element that has focus (typing, a dialog, native activation) rather than to the list. */
function isIgnored(event: KeyboardEvent): boolean {
  if (event.ctrlKey || event.metaKey || event.altKey) return true;
  const target = event.target instanceof Element ? event.target : null;
  if (!target) return false;
  if (target.closest('input, textarea, select, dialog, [contenteditable]:not([contenteditable="false"]), [role="dialog"], [role="alertdialog"]')) return true;
  return event.key === 'Enter' && target.closest('a, button') !== null;
}

/** One window keydown listener for the page: j / k / arrows move, Enter toggles the peek, r restarts, Escape closes it. */
export function useContainerKeys(options: ContainerKeysOptions): void {
  // Handlers read the latest options through a ref, so the listener is attached once.
  const latest = useRef(options);
  latest.current = options;

  useEffect(() => {
    function onKeyDown(event: KeyboardEvent) {
      const { enabled, count, focusIndex, onMove, onToggle, onRestart, onClose } = latest.current;
      if (!enabled || isIgnored(event)) return;
      switch (event.key) {
        case 'j':
        case 'ArrowDown':
          if (count > 0) onMove(Math.min(focusIndex + 1, count - 1));
          break;
        case 'k':
        case 'ArrowUp':
          if (count > 0) onMove(Math.max(focusIndex - 1, 0));
          break;
        case 'Enter':
          onToggle();
          break;
        case 'r':
          onRestart();
          break;
        case 'Escape':
          onClose();
          break;
        default:
          return;
      }
      event.preventDefault();
    }
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);
}
