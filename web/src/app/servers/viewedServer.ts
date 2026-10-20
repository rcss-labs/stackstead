// The scope of the connected server the user is looking at. The shell renders outside RemoteServer's
// provider, so it reads the scope here to describe the same server (and the same streams) as the screen.
import { readable, writable, type Readable } from '$lib/store';
import { localScope, type ServerScope } from './ServerContext';

const state = writable<ServerScope | null>(null);
export const viewedScope: Readable<ServerScope | null> = { subscribe: state.subscribe };

/** Publishes a scope as the viewed one; the returned function withdraws it unless another has replaced it. */
export function publishViewedScope(scope: ServerScope): () => void {
  state.set(scope);
  return () => state.update((current) => (current === scope ? null : current));
}

/** Stands in for the shell while a server URL has no scope (unknown server, or one still being set up): nothing of this server's is shown. */
export const noScope: ServerScope = {
  ...localScope,
  local: false,
  live: readable({ connection: 'closed', events: 'unknown', metricsDegraded: false, docker: 'unknown', compose: 'unknown', dockerVersion: '' }),
  host: readable(undefined),
  runningOps: readable([]),
};
