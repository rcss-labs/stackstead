import { useEffect, useRef, useState } from 'react';
import styles from './DiffView.module.css';

export interface DiffViewProps {
  original: string;
  modified: string;
  ariaLabel: string;
  language?: 'yaml' | 'dockerfile';
}

export function DiffView({ original, modified, ariaLabel, language = 'yaml' }: DiffViewProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const [ready, setReady] = useState(false);

  // The merge view is immutable once built, so rebuild it when either side changes.
  useEffect(() => {
    const parent = hostRef.current;
    if (!parent) return;
    let cancelled = false;
    let view: { destroy(): void } | undefined;
    import('$lib/stacks/cm')
      .then(({ createDiffView }) => {
        if (cancelled) return;
        view = createDiffView({ parent, original, modified, ariaLabel: `${ariaLabel} (unified diff)`, language });
        setReady(true);
      })
      .catch(() => {});
    return () => {
      cancelled = true;
      view?.destroy();
      setReady(false);
    };
  }, [original, modified, ariaLabel, language]);

  return (
    <div className={styles.diff} role="group" aria-label={ariaLabel} data-testid="diff-view">
      <div ref={hostRef} />
      {!ready && (
        <>
          {/* Plain-text fallback until CodeMirror loads: the proposed text, as text. */}
          <p className={styles.note}>Diff unavailable (loading); showing your draft.</p>
          <pre className={styles.fallback}>{modified}</pre>
        </>
      )}
    </div>
  );
}
