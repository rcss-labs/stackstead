import { beforeEach, describe, expect, it, vi } from 'vitest';
import { execUrl, openExec, type ExecEnd } from './exec';

class FakeSocket {
  static last: FakeSocket;
  binaryType = '';
  sent: Array<string | Uint8Array> = [];
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  onerror: (() => void) | null = null;
  closed = false;
  readyState = 1;
  constructor(public url: string) {
    FakeSocket.last = this;
  }
  send(d: string | Uint8Array) {
    this.sent.push(d);
  }
  close() {
    this.closed = true;
  }
  // test drivers
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  text(o: unknown) {
    this.onmessage?.({ data: JSON.stringify(o) });
  }
  binary(bytes: number[]) {
    this.onmessage?.({ data: new Uint8Array(bytes).buffer });
  }
  drop() {
    this.onclose?.();
  }
}

const loc = (protocol: string, host = 'example.com') => ({ protocol, host }) as Location;

function start() {
  const data: number[][] = [];
  const ends: ExecEnd[] = [];
  const warnings: number[] = [];
  const h = openExec(
    'ws://x/exec',
    {
      onData: (b) => data.push([...b]),
      onIdleWarning: (s) => warnings.push(s),
      onEnd: (e) => ends.push(e),
    },
    FakeSocket as unknown as typeof WebSocket,
  );
  const sock = FakeSocket.last;
  return { h, sock, data, ends, warnings };
}

describe('execUrl', () => {
  it('uses wss for https and repeats cmd', () => {
    const u = execUrl('', 'abc', { cmd: ['ls', '-l'], cols: 100, rows: 30 }, loc('https:'));
    expect(u).toBe('wss://example.com/api/containers/abc/exec?cmd=ls&cmd=-l&cols=100&rows=30');
  });
  it('uses ws for http, keeps the peer base, omits blank user and tty', () => {
    const u = execUrl('/api/servers/s1', 'abc', { user: '  ', cols: 80, rows: 24 }, loc('http:'));
    expect(u).toBe('ws://example.com/api/servers/s1/api/containers/abc/exec?cols=80&rows=24');
    expect(u).not.toContain('tty');
    expect(u).not.toContain('user');
  });
  it('includes user when given and encodes the container id', () => {
    const u = execUrl('', 'a b', { user: 'root:wheel', cols: 1, rows: 2 }, loc('http:'));
    expect(u).toContain('/containers/a%20b/exec');
    expect(u).toContain('user=root%3Awheel');
  });
});

describe('openExec', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it('requests arraybuffer frames', () => {
    expect(start().sock.binaryType).toBe('arraybuffer');
  });

  it('sends text as stdin frame with a 0 prefix, UTF-8 encoded', () => {
    const { h, sock } = start();
    h.send('ls');
    h.send('é');
    expect([...(sock.sent[0] as Uint8Array)]).toEqual([0, 108, 115]);
    expect([...(sock.sent[1] as Uint8Array)]).toEqual([0, 0xc3, 0xa9]);
  });

  it('sends Uint8Array as-is after the 0 prefix', () => {
    const { h, sock } = start();
    h.send(new Uint8Array([1, 2, 3]));
    expect([...(sock.sent[0] as Uint8Array)]).toEqual([0, 1, 2, 3]);
  });

  it('holds frames written while connecting until the socket opens', () => {
    const { h, sock } = start();
    sock.readyState = 0;
    h.send('a');
    expect(sock.sent).toEqual([]);
    sock.open();
    expect([...(sock.sent[0] as Uint8Array)]).toEqual([0, 97]);
  });

  it('sends resize as a JSON text frame', () => {
    const { h, sock } = start();
    h.resize(120, 40);
    expect(JSON.parse(sock.sent[0] as string)).toEqual({ type: 'resize', cols: 120, rows: 40 });
  });

  it('delivers stdout and stderr payloads without the stream id', () => {
    const { sock, data } = start();
    sock.binary([1, 104, 105]);
    sock.binary([2, 33]);
    expect(data).toEqual([[104, 105], [33]]);
  });

  it('replies to ping with pong', () => {
    const { sock } = start();
    sock.text({ type: 'ping' });
    expect(JSON.parse(sock.sent[0] as string)).toEqual({ type: 'pong' });
  });

  it('reports exit once and no lost afterwards', () => {
    const { sock, ends } = start();
    sock.text({ type: 'exit', code: 3 });
    sock.drop();
    expect(ends).toEqual([{ kind: 'exit', code: 3 }]);
  });

  it('reports closed reasons and idle warnings', () => {
    const { sock, ends, warnings } = start();
    sock.text({ type: 'idle_warning', seconds: 60 });
    sock.text({ type: 'closed', reason: 'idle' });
    sock.drop();
    expect(warnings).toEqual([60]);
    expect(ends).toEqual([{ kind: 'closed', reason: 'idle' }]);
  });

  it('reports an error frame and only the first terminal event', () => {
    const { sock, ends } = start();
    sock.text({ type: 'error', message: 'no shell found' });
    sock.text({ type: 'exit', code: 0 });
    sock.drop();
    expect(ends).toEqual([{ kind: 'error', message: 'no shell found' }]);
  });

  it('reports lost when the socket closes without a terminal frame', () => {
    const { sock, ends } = start();
    sock.open();
    sock.drop();
    expect(ends).toEqual([{ kind: 'lost' }]);
  });

  it('reports lost when the socket closes before opening', () => {
    const { sock, ends } = start();
    sock.drop();
    expect(ends).toEqual([{ kind: 'lost' }]);
  });

  it('calls the optional onOpen handler when the socket opens', () => {
    const onOpen = vi.fn();
    openExec('ws://x/exec', { onData: () => {}, onIdleWarning: () => {}, onEnd: () => {}, onOpen }, FakeSocket as unknown as typeof WebSocket);
    expect(onOpen).not.toHaveBeenCalled();
    FakeSocket.last.open();
    expect(onOpen).toHaveBeenCalledTimes(1);
  });

  it('close() closes the socket, sends nothing and fires no onEnd', () => {
    const { h, sock, ends } = start();
    h.close();
    sock.drop();
    expect(sock.closed).toBe(true);
    expect(sock.sent).toEqual([]);
    expect(ends).toEqual([]);
  });

  it('ignores sends and resizes after close', () => {
    const { h, sock } = start();
    h.close();
    h.send('x');
    h.resize(1, 1);
    expect(sock.sent).toEqual([]);
  });
});
