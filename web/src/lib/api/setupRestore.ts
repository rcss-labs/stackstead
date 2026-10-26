// Whole-host restore calls of the setup screen. Kept out of client.ts so the eager entry does not carry them.
import { ApiError, request } from './client';
import type { HostRestoreSource, HostRestoreStarted, HostRestoreSummary } from './backupTypes';

const post = <T>(path: string, body: unknown) => request<T>('POST', path, body).then((r) => r.data);

export const setupRestore = {
  inspect: (source: HostRestoreSource, setupToken?: string) =>
    post<HostRestoreSummary>('/api/setup/restore/inspect', { source, setupToken: setupToken ?? '' }),
  start: (source: HostRestoreSource, startStacks: boolean, confirmedDigest: string, setupToken?: string) =>
    post<HostRestoreStarted>('/api/setup/restore/start', { source, startStacks, confirmedDigest, setupToken: setupToken ?? '' }),
  upload: uploadRestoreArchive,
};

/** Chrome only streams a request body over HTTP/2 or newer, which a plain-HTTP localhost server is not. */
function canStreamUploads(): boolean {
  return (
    typeof window !== 'undefined' &&
    window.location.protocol === 'https:' &&
    typeof TransformStream !== 'undefined' &&
    typeof Blob.prototype.stream === 'function'
  );
}

/**
 * Sends the archive as the request body without reading it into memory. Progress is counted as the browser pulls the
 * stream; where a streamed body is not possible the Blob itself is sent and no progress is reported.
 */
async function uploadRestoreArchive(
  file: Blob,
  setupToken: string | undefined,
  onProgress: (sent: number) => void,
  signal?: AbortSignal,
): Promise<void> {
  const headers: Record<string, string> = { 'Content-Type': 'application/gzip', Accept: 'application/json' };
  if (setupToken) headers['X-Stackstead-Setup-Token'] = setupToken;
  const init: RequestInit & { duplex?: 'half' } = { method: 'POST', headers, credentials: 'same-origin', redirect: 'error', signal };
  if (canStreamUploads()) {
    let sent = 0;
    init.body = file.stream().pipeThrough(
      new TransformStream<Uint8Array, Uint8Array>({
        transform(chunk, controller) {
          sent += chunk.byteLength;
          onProgress(sent);
          controller.enqueue(chunk);
        },
      }),
    );
    init.duplex = 'half';
  } else {
    init.body = file;
  }
  let res: Response;
  try {
    res = await fetch('/api/setup/restore/upload', init);
  } catch (err) {
    if (signal?.aborted) throw signal.reason ?? err;
    throw new ApiError(0, 'network error');
  }
  if (res.ok) return;
  let parsed: { error?: unknown; code?: unknown } = {};
  try {
    parsed = (await res.json()) as typeof parsed;
  } catch {
    /* keep the status text */
  }
  throw new ApiError(
    res.status,
    typeof parsed.error === 'string' && parsed.error ? parsed.error : `HTTP ${res.status}`,
    [],
    parsed,
    typeof parsed.code === 'string' && parsed.code ? parsed.code : undefined,
  );
}
