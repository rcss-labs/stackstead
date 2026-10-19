import type { Operation } from '$lib/api/types';
import { OperationPanel } from './OperationPanel';
import { OperationsList } from './OperationsList';
import styles from './OperationsPane.module.css';

export interface OperationsPaneProps {
  operations: Operation[];
  selected: string | undefined;
  href: (id: string) => string;
  onCancel: (id: string) => Promise<void>;
  onFinished: (op: Operation) => void;
}

export function OperationsPane({ operations, selected, href, onCancel, onFinished }: OperationsPaneProps) {
  return (
    <div className={styles.pane}>
      <div className={styles.header}>
        <h2 className={styles.title}>Operations</h2>
      </div>
      <OperationsList operations={operations} selected={selected} href={href} />
      {selected && (
        <div className={styles.panel}>
          <OperationPanel key={selected} operationId={selected} onCancel={onCancel} onFinished={onFinished} />
        </div>
      )}
    </div>
  );
}
