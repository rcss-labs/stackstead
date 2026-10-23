// Browser client for the container exec WebSocket protocol (no UI).
// Binary frames: first byte is the stream id (0 stdin, 1 stdout, 2 stderr), the rest is payload.
// Text frames: JSON control messages. See the container-exec design spec, "Wire protocol".

export type ExecEnd =
  | { kind: 'exit'; code: number }
  | { kind: 'closed'; reason: 'idle' | 'session_expired' | 'shutdown' }
  | { kind: 'error'; message: string }
  | { kind: 'lost' };

export interface ExecHandlers {
  onData(bytes: Uint8Array): void;
  onIdleWarning(seconds: number): void;
  onEnd(end: ExecEnd): void;
  /** Called once the socket is open; lets a caller tell "never connected" from "connection lost". */
  onOpen?(): void;
}

export interface ExecOptions {
  cmd?: string[];
  user?: string;
  cols: number;
  rows: number;
}

export interface ExecHandle {
  send(text: string | Uint8Array): void;
  resize(cols: number, rows: number): void;
  close(): void;
}

const STREAM_STDIN = 0;
// Browsers fragment big messages and the server reads each fragment as its own
// message, so a paste is sent as separate frames that each carry the stream byte.
const MAX_STDIN_CHUNK = 16 * 1024;

export type RefusalReason = 'disabled' | 'limit' | 'not_running' | 'bad_origin' | 'peer_old' | 'unknown';

/**
 * A refused WebSocket upgrade hides its reason from the browser. The server runs the same
 * checks for a plain GET, so ask it that way. `peer` is true when the URL goes through a
 * connected server, where a bare 404 means that server predates the terminal.
 */
export async function explainRefusal(wsUrl: string, peer: boolean, fetchFn: typeof fetch = fetch): Promise<RefusalReason> {
  try {
    const res = await fetchFn(wsUrl.replace(/^ws/, 'http'), { credentials: 'same-origin' });
    let code: string | undefined;
    try {
      code = ((await res.json()) as { code?: string }).code;
    } catch {
      // no JSON body
    }
    switch (code) {
      case 'exec_disabled':
        return 'disabled';
      case 'exec_limit':
        return 'limit';
      case 'not_running':
        return 'not_running';
      case 'bad_origin':
        return 'bad_origin';
    }
    return res.status === 404 && !code && peer ? 'peer_old' : 'unknown';
  } catch {
    return 'unknown';
  }
}

/** `base` is '' locally and `/api/servers/{id}` for a peer. `tty` is left to the server default (1). */
export function execUrl(base: string, containerId: string, o: ExecOptions, loc: Location = window.location): string {
  const q = new URLSearchParams();
  for (const c of o.cmd ?? []) q.append('cmd', c);
  const user = o.user?.trim();
  if (user) q.set('user', user);
  q.set('cols', String(o.cols));
  q.set('rows', String(o.rows));
  const scheme = loc.protocol === 'https:' ? 'wss:' : 'ws:';
  return `${scheme}//${loc.host}${base}/api/containers/${encodeURIComponent(containerId)}/exec?${q}`;
}

export function openExec(url: string, h: ExecHandlers, socketCtor: typeof WebSocket = WebSocket): ExecHandle {
  const ws = new socketCtor(url);
  ws.binaryType = 'arraybuffer';
  const encoder = new TextEncoder();
  let finished = false; // a terminal event was reported, or the caller closed
  let closedByCaller = false;

  const end = (e: ExecEnd) => {
    if (finished) return;
    finished = true;
    h.onEnd(e);
  };
  // Frames written while still connecting would throw in a real WebSocket, so hold them until open.
  const pending: Array<string | Uint8Array<ArrayBuffer>> = [];
  const write = (frame: string | Uint8Array<ArrayBuffer>) => {
    if (closedByCaller || ws.readyState > 1) return;
    if (ws.readyState === 0) pending.push(frame);
    else ws.send(frame);
  };
  const sendControl = (msg: object) => write(JSON.stringify(msg));
  ws.onopen = () => {
    h.onOpen?.();
    for (const f of pending.splice(0)) ws.send(f);
  };

  ws.onmessage = (ev: MessageEvent) => {
    if (closedByCaller) return;
    if (typeof ev.data !== 'string') {
      const bytes = new Uint8Array(ev.data as ArrayBuffer);
      // stdout (1) and stderr (2) are both terminal output
      if (bytes.length > 0 && bytes[0] !== STREAM_STDIN) h.onData(bytes.subarray(1));
      return;
    }
    let msg: { type?: string; code?: number; message?: string; seconds?: number; reason?: string };
    try {
      msg = JSON.parse(ev.data);
    } catch {
      return;
    }
    switch (msg.type) {
      case 'ping':
        sendControl({ type: 'pong' });
        break;
      case 'idle_warning':
        h.onIdleWarning(Number(msg.seconds));
        break;
      case 'exit':
        end({ kind: 'exit', code: Number(msg.code) });
        break;
      case 'closed':
        end({ kind: 'closed', reason: msg.reason as 'idle' | 'session_expired' | 'shutdown' });
        break;
      case 'error':
        end({ kind: 'error', message: String(msg.message ?? '') });
        break;
    }
  };
  // A close with no terminal frame (including a refused upgrade, which browsers do not expose) is a lost connection.
  ws.onclose = () => end({ kind: 'lost' });

  return {
    send(text) {
      const payload = typeof text === 'string' ? encoder.encode(text) : text;
      for (let at = 0; at < payload.length || at === 0; at += MAX_STDIN_CHUNK) {
        const part = payload.subarray(at, at + MAX_STDIN_CHUNK);
        const frame = new Uint8Array(part.length + 1);
        frame[0] = STREAM_STDIN;
        frame.set(part, 1);
        write(frame);
      }
    },
    resize(cols, rows) {
      sendControl({ type: 'resize', cols, rows });
    },
    close() {
      if (closedByCaller) return;
      closedByCaller = true;
      finished = true;
      ws.close();
    },
  };
}
