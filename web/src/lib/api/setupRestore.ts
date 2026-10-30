// Whole-host restore calls of the setup screen. Kept out of client.ts so the eager entry does not carry them.
import { ApiError, request } from './client';
import type { HostRestoreSource, HostRestoreStarted, HostRestoreSummary } from './backupTypes';

const post = <T>(path: string, body: unknown) => request<T>('POST', path, body).then((r) => r.data);

export const setupRestore = {
  inspect: (source: HostRestoreSource, setupToken?: string, passphrase?: string) =>
    post<HostRestoreSummary>('/api/setup/restore/inspect', {
      source,
      setupToken: setupToken ?? '',
      ...(passphrase ? { passphrase } : {}),
    }),
  start: (
    source: HostRestoreSource,
    options: { startStacks: boolean; confirmedDigest: string; allowUnencrypted: boolean; passphrase?: string },
    setupToken?: string,
  ) =>
    post<HostRestoreStarted>('/api/setup/restore/start', {
      source,
      startStacks: options.startStacks,
      confirmedDigest: options.confirmedDigest,
      allowUnencrypted: options.allowUnencrypted,
      setupToken: setupToken ?? '',
      ...(options.passphrase ? { passphrase: options.passphrase } : {}),
    }),
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
  const base: RequestInit = { method: 'POST', headers, credentials: 'same-origin', redirect: 'error', signal };
  const send = (init: RequestInit) => fetch('/api/setup/restore/upload', init);
  let res: Response;
  try {
    if (canStreamUploads()) {
      let sent = 0;
      const body = file.stream().pipeThrough(
        new TransformStream<Uint8Array, Uint8Array>({
          transform(chunk, controller) {
            sent += chunk.byteLength;
            onProgress(sent);
            controller.enqueue(chunk);
          },
        }),
      );
      try {
        res = await send({ ...base, body, duplex: 'half' } as RequestInit);
      } catch (err) {
        // A streamed body needs HTTP/2; a proxy that speaks HTTP/1.1 to the browser makes fetch throw a TypeError.
        if (!(err instanceof TypeError) || signal?.aborted) throw err;
        onProgress(0); // no progress without a stream
        res = await send({ ...base, body: file });
      }
    } else {
      res = await send({ ...base, body: file });
    }
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
