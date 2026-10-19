// The stack detail page's data and state machines, ported from StackDetail.svelte's script so a React screen
// can be a thin renderer. All I/O goes through `deps` (the React hook passes the api methods and the shared
// stores); every response is sequence-guarded so a late answer never replaces a newer one, and stop() leaves
// no timer, subscription or request behind. start() after stop() resumes, so one controller survives a
// remounted effect.
import { get, writable, type Readable, type Unsubscriber } from '$lib/store';
import { ApiError } from '../api/client';
import type { Action, HealthHistory, MetricPoint, MetricsHistory, Operation, RawCompose, StackDetail } from '../api/types';
import { debounce } from '../stores/debounce';
import type { LiveChange } from '../stores/live';
import type { TrackedOperation } from '../stores/operationTracker';
import type { toasts } from '../stores/toasts';
import { detailHref } from './detailLocation';
import { TIMELINE_SPAN } from './timeline';

export interface StackDetailDeps {
  stack: (name: string) => Promise<StackDetail>;
  operations: (name: string) => Promise<Operation[]>;
  health: (name: string, p: { span: string }, signal?: AbortSignal) => Promise<HealthHistory>;
  compose: (name: string) => Promise<RawCompose>;
  metricsHistory: (
    name: string,
    q: { from: string; to: string; step: string; service: string },
    signal?: AbortSignal,
  ) => Promise<MetricsHistory>;
  act: (name: string, action: Action) => Promise<Operation>;
  remove: (name: string, withVolumes: boolean) => Promise<Operation>;
  cancel: (id: string) => Promise<unknown>;
  removeService: (name: string, service: string, hash: string) => Promise<StackDetail>;
  changes: Readable<LiveChange | null>;
  operationsRefresh: Readable<number>;
  refreshStacks: () => Promise<void>;
  /** Called after every deps.track so the operation tracker sees the new running list. */
  refreshRunningOps: () => void;
  refreshStatus: () => Promise<void>;
  track: (op: TrackedOperation) => void;
  navigate: (href: string, opts?: { replace?: boolean }) => void;
  toasts: Pick<typeof toasts, 'push'>;
  now?: () => Date;
}

/** State of the "Remove service?" dialog. */
export interface RemovalSession {
  service: string;
  hash: string;
  pending: boolean;
  error?: string;
  disabledReason?: string;
}

export const LAST_SERVICE_REASON = 'The last service cannot be removed; delete the stack instead.';

/** Why no service of this stack can be removed, or undefined when removal is allowed. */
export function removeServiceReason(detail: Pick<StackDetail, 'services'>): string | undefined {
  return detail.services.length === 1 ? LAST_SERVICE_REASON : undefined;
}

export interface StackDetailController {
  detail: Readable<StackDetail | undefined>;
  notFound: Readable<boolean>;
  loadError: Readable<string>;
  operations: Readable<Operation[]>;
  health: Readable<HealthHistory | undefined>;
  compose: Readable<RawCompose | undefined>;
  composeError: Readable<string>;
  /** Last hour of cpu per service, one slot per 2 min step, null where no sample fell. */
  cpuHour: Readable<Map<string, (number | null)[]>>;
  acting: Readable<boolean>;
  removing: Readable<RemovalSession | undefined>;
  start(): void;
  stop(): void;
  load(): Promise<StackDetail | undefined>;
  loadOperations(): Promise<void>;
  loadHealth(): Promise<void>;
  loadCompose(): Promise<void>;
  loadCpuHour(service: string): Promise<void>;
  reloadDetail(): Promise<StackDetail>;
  acceptDetail(d: StackDetail): void;
  act(action: Action): Promise<void>;
  removeStack(withVolumes: boolean): Promise<void>;
  cancelOperation(id: string): Promise<void>;
  operationFinished(op: Operation): void;
  askRemoveService(service: string): void;
  confirmRemoveService(): Promise<void>;
  cancelRemoveService(): void;
}

const CPU_STEP = '2m';
const CPU_STEP_MS = 120_000;

const errorMessage = (e: unknown) => (e instanceof Error ? e.message : String(e));
const hasStatus = (e: unknown, status: number) => e instanceof ApiError && e.status === status;

function cpuSlots(points: MetricPoint[], service: string, from: string, to: string): (number | null)[] {
  const start = Date.parse(from);
  const count = Math.round((Date.parse(to) - start) / CPU_STEP_MS);
  const slots: (number | null)[] = new Array(count).fill(null);
  for (const p of points) {
    if (p.service !== service) continue;
    const i = Math.floor((Date.parse(p.time) - start) / CPU_STEP_MS);
    if (i >= 0 && i < count) slots[i] = p.cpu;
  }
  return slots;
}

export function createStackDetail(name: string, deps: StackDetailDeps): StackDetailController {
  const now = deps.now ?? (() => new Date());

  const detail = writable<StackDetail | undefined>(undefined);
  const notFound = writable(false);
  const loadError = writable('');
  const operations = writable<Operation[]>([]);
  const health = writable<HealthHistory | undefined>(undefined);
  const compose = writable<RawCompose | undefined>(undefined);
  const composeError = writable('');
  const cpuHour = writable<Map<string, (number | null)[]>>(new Map());
  const acting = writable(false);
  const removing = writable<RemovalSession | undefined>(undefined);

  // `alive` mirrors the Svelte component's lifetime; `generation` invalidates every answer still in flight
  // when the controller stops.
  let alive = false;
  let generation = 0;
  let subscriptions: Unsubscriber[] = [];

  let seq = 0;
  async function load(): Promise<StackDetail | undefined> {
    const mine = ++seq;
    try {
      const d = await deps.stack(name);
      if (mine !== seq) return undefined;
      detail.set(d);
      notFound.set(false);
      loadError.set('');
      return d;
    } catch (e) {
      if (mine !== seq) return undefined;
      if (hasStatus(e, 404)) {
        // The stack vanished right after we asked for its removal: that is the removal succeeding.
        if (removeOpId) finishRemoval();
        else notFound.set(true);
      } else loadError.set(errorMessage(e));
      return undefined;
    }
  }

  let opsSeq = 0;
  async function loadOperations(): Promise<void> {
    const mine = ++opsSeq;
    try {
      const list = await deps.operations(name);
      if (mine === opsSeq) operations.set(list);
    } catch {
      // The list is secondary; the next refresh tries again.
    }
  }

  let healthSeq = 0;
  let healthAbort: AbortController | undefined;
  async function loadHealth(): Promise<void> {
    healthAbort?.abort();
    const controller = new AbortController();
    healthAbort = controller;
    const mine = ++healthSeq;
    try {
      const h = await deps.health(name, { span: TIMELINE_SPAN }, controller.signal);
      if (mine === healthSeq) health.set(h);
    } catch {
      // Keep the last history; the next refetch tries again.
    } finally {
      if (healthAbort === controller) healthAbort = undefined;
    }
  }

  let composeSeq = 0;
  let composeRequested = false;
  let composeInFlight: Promise<void> | undefined;
  async function fetchCompose(): Promise<void> {
    composeRequested = true;
    const mine = ++composeSeq;
    try {
      const c = await deps.compose(name);
      if (mine !== composeSeq) return;
      compose.set(c);
      composeError.set('');
    } catch (e) {
      if (mine === composeSeq) composeError.set(errorMessage(e));
    }
  }
  function loadCompose(): Promise<void> {
    if (composeInFlight) return composeInFlight;
    if (get(compose) && !get(composeError)) return Promise.resolve();
    const run = fetchCompose().finally(() => {
      if (composeInFlight === run) composeInFlight = undefined;
    });
    composeInFlight = run;
    return run;
  }

  const cpuRequested = new Set<string>();
  const cpuAborts = new Set<AbortController>();
  const cpuTokens = new Map<string, number>(); // latest request per service; older responses are dropped
  let cpuSeq = 0;
  async function loadCpuHour(service: string): Promise<void> {
    if (cpuRequested.has(service)) return;
    cpuRequested.add(service);
    const mine = generation;
    const token = ++cpuSeq;
    cpuTokens.set(service, token);
    const controller = new AbortController();
    cpuAborts.add(controller);
    // The server buckets on an epoch-aligned grid, so the window must start and end on it.
    const toMs = Math.floor(now().getTime() / CPU_STEP_MS) * CPU_STEP_MS;
    const to = new Date(toMs).toISOString().replace(/\.\d{3}Z$/, 'Z');
    const from = new Date(toMs - 3_600_000).toISOString().replace(/\.\d{3}Z$/, 'Z');
    try {
      const h = await deps.metricsHistory(name, { from, to, step: CPU_STEP, service }, controller.signal);
      if (mine !== generation || cpuTokens.get(service) !== token) return;
      cpuHour.update((m) => new Map(m).set(service, cpuSlots(h.points, service, from, to)));
    } catch {
      // Keep the last values and let the next request try again.
      if (mine === generation && cpuTokens.get(service) === token) cpuRequested.delete(service);
    } finally {
      cpuAborts.delete(controller);
    }
  }

  // A burst of this project's container events is one refetch; a resync flushes at once.
  const refetch = debounce(() => {
    cpuRequested.clear();
    for (const c of cpuAborts) c.abort();
    cpuAborts.clear();
    void load();
    void loadOperations();
    void loadHealth();
    if (composeRequested) void fetchCompose();
  });

  function start(): void {
    if (alive) return;
    alive = true;
    let firstChange = true;
    let firstTick = true;
    subscriptions = [
      deps.changes.subscribe((c) => {
        if (firstChange) {
          firstChange = false;
          void load();
          void loadOperations();
          void loadHealth();
          return;
        }
        if (!c) return;
        if (c.resync) refetch.flush();
        else if (c.project === get(detail)?.project) refetch.trigger();
      }),
      deps.operationsRefresh.subscribe(() => {
        if (firstTick) {
          firstTick = false;
          return;
        }
        void loadOperations();
      }),
    ];
  }

  function stop(): void {
    alive = false;
    refetch.cancel();
    generation++;
    seq++;
    opsSeq++;
    healthSeq++;
    composeSeq++;
    for (const unsubscribe of subscriptions) unsubscribe();
    subscriptions = [];
    healthAbort?.abort();
    healthAbort = undefined;
    for (const controller of cpuAborts) controller.abort();
    cpuAborts.clear();
    cpuRequested.clear();
    setSession(undefined);
  }

  function openOperation(o: Operation): void {
    deps.track({ id: o.id, stack: name, action: o.action });
    deps.navigate(detailHref(name, { tab: 'operations', op: o.id }));
    deps.refreshRunningOps();
    void loadOperations();
  }

  function reportActionError(e: unknown, prefix: string): void {
    if (hasStatus(e, 503)) void deps.refreshStatus();
    deps.toasts.push({ kind: hasStatus(e, 409) ? 'warning' : 'danger', message: `${prefix}: ${errorMessage(e)}` });
  }

  async function act(action: Action): Promise<void> {
    if (get(acting)) return;
    acting.set(true);
    try {
      openOperation(await deps.act(name, action));
    } catch (e) {
      reportActionError(e, `Could not run ${action}`);
    } finally {
      acting.set(false);
    }
  }

  let removeOpId: string | undefined;
  let removalDone = false;
  async function removeStack(withVolumes: boolean): Promise<void> {
    if (get(acting)) return;
    acting.set(true);
    try {
      const o = await deps.remove(name, withVolumes);
      removeOpId = o.id;
      openOperation(o);
    } catch (e) {
      reportActionError(e, 'Could not remove the stack');
    } finally {
      acting.set(false);
    }
  }

  function finishRemoval(): void {
    if (removalDone) return;
    removalDone = true;
    deps.toasts.push({ kind: 'success', message: `Removed ${name}` });
    deps.refreshRunningOps();
    void deps.refreshStacks();
    deps.navigate('/');
  }

  function operationFinished(o: Operation): void {
    deps.refreshRunningOps();
    void deps.refreshStacks();
    if (o.id === removeOpId) {
      if (o.state === 'succeeded') {
        finishRemoval();
      } else {
        removeOpId = undefined;
        deps.toasts.push({ kind: 'danger', message: `Removing ${name} ${o.state.replace('_', ' ')}` });
      }
      return;
    }
    void load();
    void loadOperations();
  }

  async function cancelOperation(id: string): Promise<void> {
    try {
      await deps.cancel(id);
    } catch (e) {
      deps.toasts.push({ kind: 'danger', message: `Could not cancel: ${errorMessage(e)}` });
    }
  }

  async function reloadDetail(): Promise<StackDetail> {
    const mine = ++seq;
    const d = await deps.stack(name);
    if (mine === seq) detail.set(d);
    return d;
  }

  function acceptDetail(d: StackDetail): void {
    seq++;
    detail.set(d);
    notFound.set(false);
    loadError.set('');
  }

  // The dialog's session is a private mutable record; ownership is its identity (as in the Svelte page),
  // and every change publishes a fresh copy so readers comparing snapshots see it.
  let session: RemovalSession | undefined;
  function setSession(next: RemovalSession | undefined): void {
    session = next;
    publishSession();
  }
  function publishSession(): void {
    if (!session) {
      removing.set(undefined);
      return;
    }
    const { service, hash, pending, error, disabledReason } = session;
    removing.set({
      service,
      hash,
      pending,
      ...(error !== undefined && { error }),
      ...(disabledReason !== undefined && { disabledReason }),
    });
  }

  function askRemoveService(service: string): void {
    const d = get(detail);
    if (!d) return;
    const disabledReason = removeServiceReason(d);
    setSession({ service, hash: d.hash, pending: false, ...(disabledReason && { disabledReason }) });
  }

  function cancelRemoveService(): void {
    setSession(undefined);
  }

  async function confirmRemoveService(): Promise<void> {
    const current = session;
    if (!current || current.pending || current.disabledReason) return;
    const { service, hash } = current;
    current.pending = true;
    current.error = undefined;
    publishSession();
    const owns = () => alive && session === current;
    try {
      const d = await deps.removeService(name, service, hash);
      void deps.refreshStacks();
      if (!alive) return;
      // A cancelled dialog still accepted a server mutation; refetch instead of replacing a newer session.
      if (!owns()) {
        void load();
        return;
      }
      acceptDetail(d);
      setSession(undefined);
      deps.toasts.push({ kind: 'success', message: `Removed ${service} from compose.yaml. Run Up to apply.` });
    } catch (e) {
      if (!owns()) return;
      const message = errorMessage(e);
      if (hasStatus(e, 409) && /changed on disk/i.test(message)) {
        await reloadAfterConflict(current, service, owns);
      } else if (hasStatus(e, 404)) {
        setSession(undefined);
        deps.toasts.push({ kind: 'warning', message });
        void load();
      } else {
        current.error = message;
        if (hasStatus(e, 409) && /depends|last service/i.test(message)) current.disabledReason = message;
        if (hasStatus(e, 503)) {
          void deps.refreshStatus();
          deps.toasts.push({ kind: 'danger', message });
        }
      }
    } finally {
      if (owns()) {
        current.pending = false;
        publishSession();
      }
    }
  }

  // The compose file changed under the dialog: reload, take the new hash and ask again, unless the reload
  // shows the service gone or now the last one.
  async function reloadAfterConflict(current: RemovalSession, service: string, owns: () => boolean): Promise<void> {
    try {
      const mine = ++seq;
      const d = await deps.stack(name);
      if (!owns()) return;
      if (mine !== seq) {
        current.error = 'The stack changed while reloading. Try again.';
        return;
      }
      acceptDetail(d);
      current.hash = d.hash;
      if (!d.services.some((s) => s.name === service)) {
        current.error = `Service "${service}" no longer exists.`;
        current.disabledReason = current.error;
      } else if (d.services.length === 1) {
        current.error = LAST_SERVICE_REASON;
        current.disabledReason = LAST_SERVICE_REASON;
      } else current.error = 'The compose file changed and was reloaded. Confirm again to remove.';
    } catch (reloadError) {
      if (owns()) {
        current.error = `Could not reload the stack: ${errorMessage(reloadError)}`;
        if (hasStatus(reloadError, 503)) void deps.refreshStatus();
      }
    }
  }

  return {
    detail: { subscribe: detail.subscribe },
    notFound: { subscribe: notFound.subscribe },
    loadError: { subscribe: loadError.subscribe },
    operations: { subscribe: operations.subscribe },
    health: { subscribe: health.subscribe },
    compose: { subscribe: compose.subscribe },
    composeError: { subscribe: composeError.subscribe },
    cpuHour: { subscribe: cpuHour.subscribe },
    acting: { subscribe: acting.subscribe },
    removing: { subscribe: removing.subscribe },
    start,
    stop,
    load,
    loadOperations,
    loadHealth,
    loadCompose,
    loadCpuHour,
    reloadDetail,
    acceptDetail,
    act,
    removeStack,
    cancelOperation,
    operationFinished,
    askRemoveService,
    confirmRemoveService,
    cancelRemoveService,
  };
}
