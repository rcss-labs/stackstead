import { useEffect, useState } from 'react';
import { Link } from 'react-router';
import type { Action } from '$lib/api/types';
import { lifecycleDisabledReason } from '$lib/stacks/lifecycle';
import { missingComposeFile } from '$lib/stacks/detailLocation';
import { setUnsavedChanges } from '$lib/stores/guard';
import { live } from '$lib/stores/live';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { EmptyState } from '../components/EmptyState';
import { Spinner } from '../components/Spinner';
import { useStore } from '../hooks/useStore';
import { DetailHeader } from './DetailHeader';
import styles from './StackDetailScreen.module.css';
import { useStackDetail } from './useStackDetail';

export function StackDetailScreen({ name }: { name: string }) {
  const { ctl, detail, notFound, loadError, operations, acting, removing } = useStackDetail(name);
  const liveState = useStore(live);
  const [retrying, setRetrying] = useState(false);
  const [pendingLeave, setPendingLeave] = useState<(() => void) | undefined>();

  useEffect(() => {
    document.title = `${name} · Stackstead`;
  }, [name]);
  useEffect(() => () => setUnsavedChanges(false), []);

  // No editor can hold unsaved changes yet; the compose and service editors raise this in later plans.
  const dirty: boolean = false;
  function guarded(action: () => void) {
    if (dirty) setPendingLeave(() => action);
    else action();
  }
  function discardAndLeave() {
    const action = pendingLeave;
    setPendingLeave(undefined);
    action?.();
  }

  async function retry() {
    setRetrying(true);
    try {
      await ctl.load();
    } finally {
      setRetrying(false);
    }
  }

  const parseError = detail?.parseError;
  const hasRunning = operations.some((o) => o.state === 'running');
  const lifecycleReason = lifecycleDisabledReason({ live: liveState, parseError });

  let body;
  if (notFound) {
    body = (
      <EmptyState title="Stack not found" description={`There is no stack named "${name}".`}>
        <Link to="/">Back to stacks</Link>
      </EmptyState>
    );
  } else if (!detail && loadError) {
    body = (
      <>
        <Banner kind="danger" title="Could not load the stack">
          {loadError}
        </Banner>
        <p>
          <Button variant="secondary" loading={retrying} onClick={() => void retry()}>
            Retry
          </Button>
        </p>
      </>
    );
  } else if (!detail) {
    body = <Spinner />;
  } else {
    const missingFile = missingComposeFile(parseError);
    body = (
      <section className={styles.page}>
        <DetailHeader
          detail={detail}
          busy={hasRunning || acting}
          lifecycleReason={lifecycleReason}
          onAct={(action: Action) => guarded(() => void ctl.act(action))}
          onRemove={(withVolumes) => guarded(() => void ctl.removeStack(withVolumes))}
        />
        {loadError && (
          <Banner kind="warning" title="Showing the last loaded state">
            {loadError}
          </Banner>
        )}
        {parseError && (
          <Banner kind="danger" title={missingFile ? 'Compose file is missing' : 'Compose file cannot be parsed'}>
            <pre className={styles.parseError}>{parseError}</pre>
            {missingFile
              ? 'Restore the file on disk to edit this stack.'
              : 'Fix it in the Compose file; the structured editor is unavailable until it parses.'}
          </Banner>
        )}
      </section>
    );
  }

  return (
    <>
      {body}
      <ConfirmDialog
        open={removing !== undefined}
        title="Remove service?"
        message={`Remove ${removing?.service ?? ''} from compose.yaml? Its container is removed on the next Up.`}
        confirmLabel="Remove service"
        danger
        pending={removing?.pending}
        confirmDisabledReason={removing?.disabledReason}
        onConfirm={() => void ctl.confirmRemoveService()}
        onCancel={ctl.cancelRemoveService}
      >
        {removing?.error && <p role="alert">{removing.error}</p>}
      </ConfirmDialog>
      <ConfirmDialog
        open={pendingLeave !== undefined}
        title="Discard unsaved changes?"
        message="You have unsaved changes in the editor. Leaving now discards them."
        confirmLabel="Discard changes"
        danger
        onConfirm={discardAndLeave}
        onCancel={() => setPendingLeave(undefined)}
      />
    </>
  );
}
