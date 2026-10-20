import { useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import type { Action } from '$lib/api/types';
import { timelineLanes } from '$lib/stacks/timeline';
import { lifecycleDisabledReason } from '$lib/stacks/lifecycle';
import { detailHref, missingComposeFile, type DetailItem } from '$lib/stacks/detailLocation';
import { setUnsavedChanges } from '$lib/stores/guard';
import { live } from '$lib/stores/live';
import { mediaQuery } from '$lib/stores/media';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { EmptyState } from '../components/EmptyState';
import { Spinner } from '../components/Spinner';
import { useIsMobile } from '../hooks/useIsMobile';
import { useStore } from '../hooks/useStore';
import { ComposeEditor } from './ComposeEditor';
import { ComposeView } from './ComposeView';
import { ConfigTab } from './ConfigTab';
import { DetailHeader } from './DetailHeader';
import { HealthTimeline } from './HealthTimeline';
import { LogConsole } from './LogConsole';
import { MetricsTab } from './MetricsTab';
import { OperationsPane } from './OperationsPane';
import { ServiceChips, ServiceRail } from './ServiceRail';
import { ServicePane } from './ServicePane';
import { SplitPane } from './SplitPane';
import styles from './StackDetailScreen.module.css';
import { useDetailLocation } from './useDetailLocation';
import { useLiveMetrics } from './useLiveMetrics';
import { useNowSeconds } from './useNowSeconds';
import { useStackDetail } from './useStackDetail';

// README: the split pane is two columns from 760px; keep in step with the 759px rules in the pane modules.
const wideQuery = mediaQuery('(min-width: 760px)');

export function StackDetailScreen({ name }: { name: string }) {
  const { ctl, detail, notFound, loadError, operations, health, compose, composeError, cpuHour, acting, removing } = useStackDetail(name);
  const location = useDetailLocation(name, detail);
  const nowSeconds = useNowSeconds(60_000);
  const liveState = useStore(live);
  const wide = useStore(wideQuery);
  const { item, serviceTab } = location;
  const parseErrorText = detail?.parseError;
  const selectedService = item.kind === 'service' ? detail?.services.find((svc) => svc.name === item.name) : undefined;
  // One stream feeds the rail (wide screens) and the Metrics tab (any width); never while the page has no data.
  const liveMetrics = useLiveMetrics(name, !!detail && !parseErrorText && (wide || (item.kind === 'service' && serviceTab === 'metrics')));
  const latest = useStore(liveMetrics.latest);
  const [retrying, setRetrying] = useState(false);
  const [pendingLeave, setPendingLeave] = useState<(() => void) | undefined>();
  const mobile = useIsMobile();
  const [query] = useSearchParams();
  const navigate = useNavigate();
  const [logsOpen, setLogsOpen] = useState(!mobile);
  const [logService, setLogService] = useState<string | undefined>();
  const consoleSlot = useRef<HTMLDivElement>(null);
  // ?tab=logs (from a link or a reload) opens the console on the requested service and moves focus to it.
  // The state is adjusted while rendering so the console's first stream already carries that service.
  const logsRequested = location.logsRequested;
  const search = query.toString();
  const [syncedSearch, setSyncedSearch] = useState<string | undefined>();
  if (logsRequested && syncedSearch !== search) {
    setSyncedSearch(search);
    setLogsOpen(true);
    setLogService(location.logService);
  } else if (!logsRequested && syncedSearch !== undefined) {
    setSyncedSearch(undefined); // leaving ?tab=logs re-arms it, so Back re-expands the console
  }
  useEffect(() => {
    if (logsRequested && logsOpen) consoleSlot.current?.querySelector<HTMLElement>('button[aria-expanded]')?.focus();
  }, [logsRequested, search, logsOpen]);

  useEffect(() => {
    document.title = `${name} · Stackstead`;
  }, [name]);
  useEffect(() => () => setUnsavedChanges(false), []);
  // The compose editor is the only producer of unsaved changes for now (18e adds the service and add flags).
  const parseErrorPresent = !!detail?.parseError;
  const rawOnly = parseErrorPresent && !missingComposeFile(detail?.parseError);
  const [editRequested, setEditRequested] = useState(false);
  const [composeDirty, setComposeDirty] = useState(false);
  const onCompose = item.kind === 'compose';
  const editingCompose = onCompose && (editRequested || rawOnly);
  // Set when the user asked to edit, so the editor takes keyboard focus once loaded.
  const [focusEditor, setFocusEditor] = useState(false);
  // Both loaders are idempotent; they run again when a refetch replaces the detail because that clears the
  // cpu-hour cache (the controller refreshes a requested compose file itself). The compose editor fetches the
  // file itself, so the screen stays out of its way while editing.
  useEffect(() => {
    if (editingCompose) return;
    if (item.kind === 'compose' || (item.kind === 'service' && serviceTab === 'config')) void ctl.loadCompose();
  }, [ctl, item.kind, serviceTab, detail, editingCompose]);
  const overviewService = selectedService && serviceTab === 'overview' ? selectedService.name : undefined;
  useEffect(() => {
    if (overviewService) void ctl.loadCpuHour(overviewService);
  }, [ctl, overviewService, detail]);

  const dirty = editingCompose && composeDirty;
  useEffect(() => {
    setUnsavedChanges(dirty);
  }, [dirty]);
  // A parse error opens the raw editor and latches edit mode: once the file is fixed (saved here or on disk)
  // the parse error clears, but the editor, its Apply button and any draft must stay until the user leaves.
  useEffect(() => {
    if (!onCompose || !rawOnly) return;
    setEditRequested(true);
    // Compose is only the default item while the file fails to parse; pin it in the URL so a fix keeps it selected.
    if (query.get('tab') !== 'compose') void navigate(location.hrefFor({ kind: 'compose' }), { replace: true });
  }, [onCompose, rawOnly, query, navigate, location.hrefFor]);
  // Leaving the compose item ends edit mode; a held draft is gone with the editor.
  useEffect(() => {
    if (!onCompose) {
      setEditRequested(false);
      setFocusEditor(false);
      setComposeDirty(false);
    }
  }, [onCompose]);
  function guarded(action: () => void) {
    if (dirty) setPendingLeave(() => action);
    else action();
  }
  function discardAndLeave() {
    const action = pendingLeave;
    setPendingLeave(undefined);
    setEditRequested(false);
    setFocusEditor(false);
    setComposeDirty(false);
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
    const railProps = {
      services: parseError ? [] : detail.services,
      selected: item,
      operationsCount: operations.length,
      latest,
      onSelect: (target: DetailItem) => guarded(() => location.go(location.hrefFor(target))),
    };
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
        {/* Under a parse error there are no services to chart, so the card would be an empty frame. */}
        {!parseError && (
          <HealthTimeline
            lanes={timelineLanes(health, detail.services.map((s) => s.name), nowSeconds)}
            selected={location.item.kind === 'service' ? location.item.name : undefined}
            onSelect={(service) => guarded(() => location.go(location.hrefFor({ kind: 'service', name: service })))}
          />
        )}
        <SplitPane
          rail={<ServiceRail {...railProps} />}
          chips={<ServiceChips {...railProps} />}
          console={
            // A parse error leaves no services to read logs from, so no console and no stream.
            !parseError && (
              <div ref={consoleSlot}>
                <LogConsole
                  source={{ kind: 'stack', name }}
                  services={detail.services.map((s) => s.name)}
                  service={logService}
                  onService={setLogService}
                  open={logsOpen}
                  onOpenChange={setLogsOpen}
                  mobile={mobile}
                />
              </div>
            )
          }
        >
          {item.kind === 'service' && selectedService ? (
            <ServicePane
              service={selectedService}
              tab={serviceTab}
              onTab={(tab) => guarded(() => location.go(location.hrefFor(item, tab)))}
              onLogs={() => {
                setLogService(selectedService.name);
                setLogsOpen(true);
              }}
              latestCpu={latest.get(selectedService.name)?.cpu}
              cpuHour={cpuHour.get(selectedService.name)}
            >
              {(tab) =>
                tab === 'metrics' ? (
                  <MetricsTab
                    key={selectedService.name}
                    stackName={name}
                    service={selectedService.name}
                    live={liveMetrics}
                    metricsDegraded={liveState.metricsDegraded}
                    metricsIntervalSeconds={liveState.metricsIntervalSeconds}
                    metricsRetentionSeconds={liveState.metricsRetentionSeconds}
                    allServices={detail.services.map((s) => s.name)}
                  />
                ) : tab === 'config' ? (
                  <ConfigTab
                    service={selectedService}
                    compose={compose}
                    error={composeError}
                    onRetry={() => void ctl.loadCompose()}
                    onEditYaml={() =>
                      guarded(() => {
                        location.go(location.hrefFor({ kind: 'compose' }));
                        setEditRequested(true);
                        setFocusEditor(true);
                      })
                    }
                  />
                ) : null
              }
            </ServicePane>
          ) : item.kind === 'compose' ? (
            <ComposeView
              detail={detail}
              compose={compose}
              error={composeError}
              onRetry={() => void ctl.loadCompose()}
              editSlot={
                !editingCompose &&
                !missingFile && (
                  <Button variant="secondary" size="sm" onClick={() => {
                      setEditRequested(true);
                      setFocusEditor(true);
                    }}>
                    Edit YAML
                  </Button>
                )
              }
              editor={
                editingCompose && (
                  <ComposeEditor
                    key={name}
                    stackName={name}
                    composePath={detail.path}
                    onDirtyChange={setComposeDirty}
                    autoFocus={focusEditor}
                    onSaved={() => void ctl.load()}
                    onApply={() => void ctl.act('up')}
                  />
                )
              }
            />
          ) : item.kind === 'operations' ? (
            <OperationsPane
              operations={operations}
              selected={location.op}
              href={(id) => detailHref(name, { tab: 'operations', op: id })}
              onCancel={ctl.cancelOperation}
              onFinished={ctl.operationFinished}
            />
          ) : item.kind === 'none' ? (
            <EmptyState title="No services" description="This stack has no services yet." />
          ) : null}
        </SplitPane>
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
