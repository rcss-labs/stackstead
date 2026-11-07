import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router';
import type { Action } from '$lib/api/types';
import { timelineLanes } from '$lib/stacks/timeline';
import { lifecycleDisabledReason } from '$lib/stacks/lifecycle';
import { detailHref, missingComposeFile, type DetailItem } from '$lib/stacks/detailLocation';
import { removeServiceReason } from '$lib/stacks/detail';
import { backupName } from '$lib/stacks/composeText';
import { ApiError } from '$lib/api/client';
import { toasts } from '$lib/stores/toasts';
import { setUnsavedChanges } from '$lib/stores/guard';
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
import { AddServicePanel } from './AddServicePanel';
import { ServiceEditor } from './ServiceEditor';
import { ServicePane } from './ServicePane';
import { SplitPane } from './SplitPane';
import styles from './StackDetailScreen.module.css';
import { useDetailLocation } from './useDetailLocation';
import { useLiveMetrics } from './useLiveMetrics';
import { useNowSeconds } from './useNowSeconds';
import { useStackDetail } from './useStackDetail';

import { FEATURE_COMPOSE_RESTORE, useServer } from '../servers/ServerContext';
// Lazy: keeps the Dockerfile editor out of the entry chunk.
const DockerfilePanel = lazy(() => import('./DockerfilePanel').then((m) => ({ default: m.DockerfilePanel })));
// README: the split pane is two columns from 760px; keep in step with the 759px rules in the pane modules.
const wideQuery = mediaQuery('(min-width: 760px)');

export function StackDetailScreen({ name }: { name: string }) {
  const { live, href: serverHref, api, features } = useServer();
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
  // The dock expanded over the service pane; remembered per item, so selecting anything else ends it.
  const [expandedOn, setExpandedOn] = useState<string | undefined>();
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

  const [editingService, setEditingService] = useState<string | undefined>();
  const [serviceDirty, setServiceDirty] = useState(false);
  // The service editor counts only while it is open on the selected service.
  const serviceEditorOpen = item.kind === 'service' && editingService === item.name;
  const [dockerfileDirty, setDockerfileDirty] = useState(false);
  const [addingService, setAddingService] = useState(false);
  const [addDirty, setAddDirty] = useState(false);
  const dirty = (editingCompose && composeDirty) || (serviceEditorOpen && serviceDirty) || (addingService && addDirty) ||
    (item.kind === 'service' && serviceTab === 'dockerfile' && dockerfileDirty);
  const closeAdd = () => {
    setAddingService(false);
    setAddDirty(false);
  };
  // The add panel belongs to the item it was opened on and to a parseable file.
  const itemKey = item.kind === 'service' ? `service:${item.name}` : item.kind;
  const [addOpenedOn, setAddOpenedOn] = useState<string | undefined>();
  const expanded = !mobile && !parseErrorText && item.kind === 'service' && expandedOn === itemKey;
  useEffect(() => {
    if (addingService && (!!parseErrorText || itemKey !== addOpenedOn)) closeAdd();
  }, [addingService, parseErrorText, itemKey, addOpenedOn]);
  // Selecting anything but the edited service ends the edit; its draft goes with the editor.
  useEffect(() => {
    if (!serviceEditorOpen) {
      setEditingService(undefined);
      setServiceDirty(false);
    }
  }, [serviceEditorOpen]);
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
    setEditingService(undefined);
    setServiceDirty(false);
    setDockerfileDirty(false);
    closeAdd();
    action?.();
  }

  // ?edit=1 on a service URL (linked from the overview) opens that service's editor, through the same guard as
  // the Edit button. The parameter is consumed either way, so a reload or Back does not reopen the editor.
  const editParam = query.get('edit') === '1';
  useEffect(() => {
    if (!editParam || !detail) return;
    const next = new URLSearchParams(query);
    next.delete('edit');
    void navigate({ search: next.toString() }, { replace: true });
    if (parseErrorText || item.kind !== 'service' || !selectedService) return;
    const target = selectedService.name;
    guarded(() => {
      closeAdd();
      setEditingService(target);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editParam, detail]);

  // Restore .bak: offered on a parse error when the server can restore and a .bak exists. The compose file is read
  // once more here for its `hasBackup` and hash, because the raw editor keeps its own copy.
  const restoreSupported = features.includes(FEATURE_COMPOSE_RESTORE);
  const [backup, setBackup] = useState<{ hash: string } | undefined>();
  const [restoreEpoch, setRestoreEpoch] = useState(0);
  const [restore, setRestore] = useState<{ pending: boolean; error?: string } | undefined>();
  const checkBackup = rawOnly && restoreSupported;
  useEffect(() => {
    if (!checkBackup) {
      setBackup(undefined);
      return;
    }
    let current = true;
    api
      .compose(name)
      .then((raw) => current && setBackup(raw.hasBackup ? { hash: raw.hash } : undefined))
      .catch(() => current && setBackup(undefined));
    return () => {
      current = false;
    };
  }, [checkBackup, name, api, restoreEpoch]);
  const composeName = backupName(detail?.path).replace(/\.bak$/, '');
  async function confirmRestore() {
    if (!backup) return;
    setRestore({ pending: true });
    try {
      await api.restoreComposeBackup(name, backup.hash);
    } catch (err) {
      const status = err instanceof ApiError ? err.status : 0;
      setRestore({
        pending: false,
        error: status === 409 ? 'The file changed since it was loaded. Nothing was restored; reload the page and try again.' : String(err),
      });
      return;
    }
    setRestore(undefined);
    setComposeDirty(false);
    setRestoreEpoch((n) => n + 1); // the raw editor reads the restored file afresh
    toasts.push({ kind: 'success', message: `Restored ${composeName} from its .bak` });
    await ctl.load();
  }

  function openAdd() {
    guarded(() => {
      setEditingService(undefined);
      setServiceDirty(false);
      setAddOpenedOn(itemKey);
      setAddingService(true);
    });
  }

  async function retry() {
    setRetrying(true);
    try {
      await ctl.load();
    } finally {
      setRetrying(false);
    }
  }

  // A removed service that the URL still names sends the page to the first remaining service. The name is
  // remembered from the dialog until the refreshed detail no longer lists it, so a 404 or a cancelled-but-accepted
  // removal (the controller clears the dialog before load() replaces the detail) is covered too.
  const removedService = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (removing) {
      removedService.current = removing.service;
      return;
    }
    const gone = removedService.current;
    if (!gone) return;
    if (query.get('service') !== gone) {
      removedService.current = undefined; // the page already moved elsewhere
      return;
    }
    const first = detail?.services[0];
    if (first && !detail.services.some((svc) => svc.name === gone)) {
      removedService.current = undefined;
      void navigate(location.hrefFor({ kind: 'service', name: first.name }), { replace: true });
    }
  }, [removing, detail, query, navigate, location.hrefFor]);

  const parseError = detail?.parseError;
  const hasRunning = operations.some((o) => o.state === 'running');
  const lifecycleReason = lifecycleDisabledReason({ live: liveState, parseError });

  let body;
  if (notFound) {
    body = (
      <EmptyState title="Stack not found" description={`There is no stack named "${name}".`}>
        <Link to={serverHref('/')}>Back to stacks</Link>
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
      onSelect: (target: DetailItem) =>
        guarded(() => {
          closeAdd();
          location.go(location.hrefFor(target));
        }),
      adding: addingService,
      addSlot:
        parseError || serviceEditorOpen ? undefined : (
          <span className={styles.addEntry}>
            <Button
              variant="ghost"
              size="xs"
              aria-label="Add service"
              aria-current={addingService ? 'true' : undefined}
              data-current={addingService}
              onClick={() => {
                if (!addingService) openAdd();
              }}
            >
              + Add
            </Button>
          </span>
        ),
    };
    body = (
      <section className={styles.page}>
        <DetailHeader
          detail={detail}
          busy={hasRunning || acting}
          lifecycleReason={lifecycleReason}
          onAct={(action: Action) => guarded(() => void ctl.act(action))}
          onRemove={(withVolumes) => guarded(() => void ctl.removeStack(withVolumes))}
          onViewCompose={() =>
            guarded(() => {
              closeAdd();
              location.go(location.hrefFor({ kind: 'compose' }));
            })
          }
        />
        {(loadError || parseError) && <div className={styles.banners}>
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
            {backup && (
              <p className={styles.restore}>
                <Button variant="secondary" size="sm" onClick={() => guarded(() => setRestore({ pending: false }))}>
                  Restore .bak
                </Button>
              </p>
            )}
          </Banner>
        )}
        </div>}
        {/* Under a parse error there are no services to chart, so the card would be an empty frame. */}
        {!parseError && (
          <HealthTimeline
            lanes={timelineLanes(health, detail.services.map((s) => s.name), nowSeconds)}
            selected={location.item.kind === 'service' ? location.item.name : undefined}
            onSelect={(service) =>
              guarded(() => {
                closeAdd();
                location.go(location.hrefFor({ kind: 'service', name: service }));
              })
            }
          />
        )}
        <SplitPane
          rail={<ServiceRail {...railProps} />}
          chips={<ServiceChips {...railProps} />}
          expanded={expanded}
          console={
            // A parse error leaves no services to read logs from, so no console and no stream. Phones read logs
            // in the Logs tab instead of a dock.
            !parseError &&
            !mobile && (
              <div ref={consoleSlot}>
                <LogConsole
                  source={{ kind: 'stack', name }}
                  services={detail.services.map((s) => s.name)}
                  service={logService}
                  onService={setLogService}
                  open={logsOpen}
                  onOpenChange={setLogsOpen}
                  mobile={false}
                  fill={expanded}
                  onExpandedChange={
                    item.kind === 'service'
                      ? (on) => {
                          setExpandedOn(on ? itemKey : undefined);
                          if (on) setLogsOpen(true);
                        }
                      : undefined
                  }
                />
              </div>
            )
          }
        >
          {addingService && !parseError ? (
            <AddServicePanel
              key={name}
              stack={detail}
              onDirtyChange={setAddDirty}
              onReload={ctl.reloadDetail}
              onCancel={() => guarded(closeAdd)}
              onAdded={(added, _partial, service) => {
                const selected = service || added.services.at(-1)?.name;
                ctl.acceptDetail(added);
                closeAdd();
                if (selected) location.go(location.hrefFor({ kind: 'service', name: selected }));
              }}
            />
          ) : item.kind === 'service' && selectedService ? (
            <ServicePane
              service={selectedService}
              tab={mobile && logsRequested ? 'logs' : serviceTab}
              onTab={(tab) =>
                guarded(() =>
                  location.go(tab === 'logs' ? serverHref(detailHref(name, { service: selectedService.name, tab: 'logs' })) : location.hrefFor(item, tab)),
                )
              }
              logs={
                mobile && (
                  <LogConsole
                    source={{ kind: 'stack', name }}
                    services={detail.services.map((s) => s.name)}
                    service={logService}
                    onService={setLogService}
                    open
                    onOpenChange={setLogsOpen}
                    mobile
                    fill
                  />
                )
              }
              onLogs={() => {
                const open = () => {
                  setLogService(selectedService.name);
                  setLogsOpen(true);
                };
                // Only the phone navigates (to the Logs tab) and so can drop a draft; the wide dock opens beside it.
                if (!mobile) return open();
                guarded(() => {
                  open();
                  location.go(serverHref(detailHref(name, { service: selectedService.name, tab: 'logs' })));
                });
              }}
              onEdit={parseError ? undefined : () => guarded(() => setEditingService(selectedService.name))}
              onCloseEditor={() => guarded(() => setEditingService(undefined))}
              editor={
                serviceEditorOpen && (
                  <ServiceEditor
                    key={`${name}/${selectedService.name}`}
                    stack={detail}
                    service={selectedService}
                    onSaved={() => void ctl.load()}
                    onApplied={() => void ctl.loadOperations()}
                    onRawEditor={() =>
                      guarded(() => {
                        setEditingService(undefined);
                        location.go(location.hrefFor({ kind: 'compose' }));
                        setEditRequested(true);
                        setFocusEditor(true);
                      })
                    }
                    onReload={ctl.reloadDetail}
                    lifecycleDisabledReason={lifecycleReason}
                    onDirtyChange={setServiceDirty}
                    removeSlot={
                      <Button
                        variant="danger"
                        size="sm"
                        aria-label={`Remove service ${selectedService.name}`}
                        disabledReason={removeServiceReason(detail)}
                        onClick={() => guarded(() => ctl.askRemoveService(selectedService.name))}
                      >
                        Remove service
                      </Button>
                    }
                  />
                )
              }
              onStart={() => guarded(() => void ctl.act('up'))}
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
                ) : tab === 'dockerfile' ? (
                  <Suspense fallback={<Spinner />}>
                    <DockerfilePanel key={`${name}/${selectedService.name}`} stackName={name} service={selectedService.name} onDirtyChange={setDockerfileDirty} />
                  </Suspense>
                ) : tab === 'config' ? (
                  <ConfigTab
                    service={selectedService}
                    compose={compose}
                    error={composeError}
                    onRetry={() => void ctl.loadCompose()}
                    services={detail.services.map((s) => s.name)}
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
                    key={`${name}/${restoreEpoch}`}
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
              href={(id) => serverHref(detailHref(name, { tab: 'operations', op: id }))}
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
        open={restore !== undefined}
        title={`Restore ${composeName}.bak?`}
        message={`${composeName}.bak replaces the current file, which is kept as ${composeName}.broken.`}
        confirmLabel="Restore file"
        pending={restore?.pending}
        onConfirm={() => void confirmRestore()}
        onCancel={() => setRestore(undefined)}
      >
        {restore?.error && <p role="alert">{restore.error}</p>}
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
