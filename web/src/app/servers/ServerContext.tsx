import { createContext, useContext } from 'react';
import { api } from '$lib/api/client';
import type { Api } from '$lib/api/client';
import type { DockerEvent, HostSnapshot, Operation } from '$lib/api/types';
import type { Readable } from '$lib/store';
import { openSSE } from '$lib/sse/client';
import { host, hostHistory } from '$lib/stores/host';
import type { HostSample } from '$lib/stores/host';
import { changes, events, live, refreshStatus } from '$lib/stores/live';
import type { LiveChange, LiveState } from '$lib/stores/live';
import { trackOperation } from '$lib/stores/operationTracker';
import { runningOps } from '$lib/stores/runningOps';
import { operationsRefresh, refreshRunningOps, refreshStacks, stacks } from '$lib/stores/stacks';
import type { StacksState } from '$lib/stores/stacks';
import type { TrackedOperation } from '$lib/stores/operationTracker';

/** Optional capabilities a server advertises in its hello; the names match the Go side. */
export const FEATURE_YAML_CREATE = 'yaml-create';
export const FEATURE_MANAGED_DOCKERFILE = 'managed-dockerfile';
export const ALL_FEATURES: readonly string[] = [FEATURE_YAML_CREATE, FEATURE_MANAGED_DOCKERFILE];

/** Everything a screen needs to show one server: its API, live streams and stores, and how to link inside it. */
export interface ServerScope {
  local: boolean;
  /** Peer id; 'local' for this server. */
  id: string;
  /** URL slug under /s/; '' for this server. */
  slug: string;
  name: string;
  /** What this server supports beyond the basics. This server has them all; a peer has only what it advertised, so never assume the gateway's own. */
  features: readonly string[];
  /** Scoped to this server's endpoints; the local-only peering members live on `api` from `$lib/api/client`. */
  api: Api;
  open: typeof openSSE;
  live: Readable<LiveState>;
  changes: Readable<LiveChange | null>;
  events: Readable<DockerEvent[]>;
  refreshStatus: () => Promise<void>;
  stacks: Readable<StacksState>;
  refreshStacks: () => Promise<void>;
  host: Readable<HostSnapshot | undefined>;
  hostHistory: Readable<HostSample[]>;
  runningOps: Readable<Operation[]>;
  /** Asks the running-operations list to re-check; call after trackOperation. */
  refreshRunningOps: () => void;
  trackOperation: (op: TrackedOperation) => number;
  /** Ticks when an operation may have ended; screens re-check their data on each tick. */
  operationsRefresh: Readable<number>;
  /** Maps an app path ('/stacks/x') into this server's URL space ('/s/mini-02/stacks/x'). */
  href: (path: string) => string;
}

export const localScope: ServerScope = {
  local: true,
  id: 'local',
  slug: '',
  name: 'this server',
  features: ALL_FEATURES,
  api,
  open: openSSE,
  live,
  changes,
  events,
  refreshStatus,
  stacks,
  refreshStacks,
  host,
  hostHistory,
  runningOps,
  refreshRunningOps,
  operationsRefresh,
  trackOperation,
  href: (path) => path,
};

/** The default is the local scope, so screens outside a provider behave as they always have. */
export const ServerContext = createContext<ServerScope>(localScope);

export function useServer(): ServerScope {
  return useContext(ServerContext);
}

/** Whether the viewed server supports a feature. */
export function useServerFeature(feature: string): boolean {
  return useServer().features.includes(feature);
}
