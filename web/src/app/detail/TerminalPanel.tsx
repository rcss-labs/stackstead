import { Suspense, lazy } from 'react';
import type { ContainerInfo } from '$lib/api/types';
import { EmptyState } from '../components/EmptyState';
import { Field } from '../components/Field';
import { Spinner } from '../components/Spinner';
import styles from './TerminalPanel.module.css';

// xterm is large, so it loads only when the Terminal tab opens.
const TerminalTab = lazy(() => import('../containers/TerminalTab'));

export interface TerminalPanelProps {
  containers: ContainerInfo[];
  /** The chosen container id; an unknown or missing id falls back to the first running one. */
  selectedId: string | undefined;
  onSelect: (id: string) => void;
  onStart: () => void;
}

export function TerminalPanel({ containers, selectedId, onSelect, onStart }: TerminalPanelProps) {
  const container = containers.find((c) => c.id === selectedId) ?? containers.find((c) => c.state === 'running') ?? containers[0];
  if (!container) {
    return (
      <div className={styles.panel}>
        <EmptyState title="No container for this service. Start the stack to open a terminal." />
      </div>
    );
  }
  return (
    <div className={styles.panel}>
      {containers.length > 1 && (
        <div className={styles.picker}>
          <Field label="Container" id="service-terminal-container" mono>
            {(control) => (
              <select {...control} disabled={control.readOnly} value={container.id} onChange={(e) => onSelect(e.target.value)}>
                {containers.map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
              </select>
            )}
          </Field>
        </div>
      )}
      <Suspense fallback={<Spinner />}>
        <TerminalTab key={container.id} containerId={container.id} containerName={container.name} running={container.state === 'running'} onStart={onStart} />
      </Suspense>
    </div>
  );
}
