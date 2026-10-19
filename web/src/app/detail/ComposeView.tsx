import type { ReactNode } from 'react';
import type { RawCompose, StackDetail } from '$lib/api/types';
import { composeFileName } from '$lib/stacks/composeText';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Spinner } from '../components/Spinner';
import styles from './ComposeView.module.css';

export interface ComposeViewProps {
  detail: StackDetail;
  compose: RawCompose | undefined;
  error: string;
  onRetry: () => void;
  /** Where the Edit YAML control goes (a later plan fills it). */
  editSlot?: ReactNode;
}

export function ComposeView({ detail, compose, error, onRetry, editSlot }: ComposeViewProps) {
  return (
    <div className={styles.view}>
      <header className={styles.header}>
        <h2 className={styles.title}>{composeFileName(detail.path)}</h2>
        <span className={styles.path}>{detail.path}</span>
        <span className={styles.spacer} />
        {editSlot}
      </header>
      {detail.parseError && (
        <Banner kind="warning" title="This file cannot be edited with forms">
          Edit it as text; the forms return once it parses.
        </Banner>
      )}
      {error ? (
        <div className={styles.body}>
          <Banner kind="danger" title="Could not load the compose file">
            {error}
          </Banner>
          <p>
            <Button variant="secondary" onClick={onRetry}>
              Retry
            </Button>
          </p>
        </div>
      ) : compose === undefined ? (
        <div className={styles.body}>
          <Spinner />
        </div>
      ) : (
        <pre className={styles.text}>{compose.content}</pre>
      )}
    </div>
  );
}
