import { act, cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ServerContext, localScope, type ServerScope } from '../servers/ServerContext';

const term = vi.hoisted(() => ({
  instances: [] as Array<{ written: string[]; emitInput: (d: string) => void; disposed: boolean }>,
}));

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options: Record<string, unknown> = {};
    written: string[] = [];
    disposed = false;
    private inputHandlers: Array<(d: string) => void> = [];
    constructor() {
      term.instances.push(this);
    }
    loadAddon() {}
    open() {}
    focus() {}
    write(d: string | Uint8Array) {
      this.written.push(typeof d === 'string' ? d : new TextDecoder().decode(d));
    }
    onData(fn: (d: string) => void) {
      this.inputHandlers.push(fn);
      return { dispose() {} };
    }
    emitInput(d: string) {
      for (const fn of this.inputHandlers) fn(d);
    }
    dispose() {
      this.disposed = true;
    }
  },
}));
vi.mock('@xterm/addon-fit', () => ({ FitAddon: class { fit() {} } }));

import TerminalTab from './TerminalTab';

class FakeSocket {
  static all: FakeSocket[] = [];
  binaryType = '';
  readyState = 0;
  sent: unknown[] = [];
  closed = false;
  onopen: (() => void) | null = null;
  onmessage: ((e: { data: unknown }) => void) | null = null;
  onclose: (() => void) | null = null;
  constructor(public url: string) {
    FakeSocket.all.push(this);
  }
  send(d: unknown) {
    this.sent.push(d);
  }
  close() {
    this.closed = true;
  }
  open() {
    this.readyState = 1;
    this.onopen?.();
  }
  text(o: unknown) {
    this.onmessage?.({ data: JSON.stringify(o) });
  }
  drop() {
    this.onclose?.();
  }
}

const peerScope: ServerScope = { ...localScope, local: false, id: 'mini-02', slug: 'mini-02', name: 'mini-02' };

function mount(over: Partial<{ running: boolean; onStart: () => void; scope: ServerScope }> = {}) {
  const onStart = over.onStart ?? vi.fn();
  const view = render(
    <MemoryRouter>
      <ServerContext.Provider value={over.scope ?? localScope}>
        <TerminalTab containerId="abc123" containerName="redis" running={over.running ?? true} onStart={onStart} />
      </ServerContext.Provider>
    </MemoryRouter>,
  );
  return { ...view, onStart };
}
const lastSocket = () => FakeSocket.all[FakeSocket.all.length - 1];
const params = (s: FakeSocket) => new URL(s.url.replace(/^ws/, 'http')).searchParams;
const lastTerm = () => term.instances[term.instances.length - 1];
const written = () => lastTerm().written.join('');

beforeEach(() => {
  FakeSocket.all = [];
  term.instances = [];
  try {
    localStorage.clear();
  } catch {
    // storage may be unavailable
  }
  vi.stubGlobal('WebSocket', FakeSocket);
});
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe('TerminalTab', () => {
  it('offers Start and opens no socket when the container is stopped', async () => {
    const { onStart } = mount({ running: false });
    expect(screen.getByText("Container isn't running")).toBeTruthy();
    await userEvent.setup().click(screen.getByRole('button', { name: 'Start' }));
    expect(onStart).toHaveBeenCalledTimes(1);
    expect(FakeSocket.all).toHaveLength(0);
  });

  it('opens a local socket for the container with the terminal size', () => {
    mount();
    const url = new URL(lastSocket().url.replace(/^ws/, 'http'));
    expect(url.pathname).toBe('/api/containers/abc123/exec');
    expect(url.searchParams.get('cols')).toBe('80');
    expect(url.searchParams.get('rows')).toBe('24');
  });

  it('uses the peer base on a connected server', () => {
    mount({ scope: peerScope });
    expect(new URL(lastSocket().url.replace(/^ws/, 'http')).pathname).toBe('/api/servers/mini-02/api/containers/abc123/exec');
  });

  it('applies the shell, custom command and user choices when restarting', async () => {
    const user = userEvent.setup();
    mount();
    await user.selectOptions(screen.getByLabelText('Shell'), 'sh');
    await user.type(screen.getByLabelText('User'), 'root');
    await user.click(screen.getByRole('button', { name: /restart/i }));
    expect(params(lastSocket()).getAll('cmd')).toEqual(['/bin/sh']);
    expect(params(lastSocket()).get('user')).toBe('root');

    await user.selectOptions(screen.getByLabelText('Shell'), 'custom');
    await user.type(screen.getByLabelText('Command'), 'python -i');
    await user.click(screen.getByRole('button', { name: /restart/i }));
    expect(params(lastSocket()).getAll('cmd')).toEqual(['python', '-i']);
  });

  it('remembers the choices per container name', async () => {
    const user = userEvent.setup();
    mount();
    await user.selectOptions(screen.getByLabelText('Shell'), 'sh');
    cleanup();
    mount();
    expect((screen.getByLabelText('Shell') as HTMLSelectElement).value).toBe('sh');
    expect(params(lastSocket()).getAll('cmd')).toEqual(['/bin/sh']);
  });

  it('still renders when localStorage throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked');
    });
    mount();
    expect(screen.getByLabelText('Shell')).toBeTruthy();
    expect(FakeSocket.all).toHaveLength(1);
  });

  it('writes the exit code and reconnects on demand', async () => {
    mount();
    act(() => {
      lastSocket().open();
      lastSocket().text({ type: 'exit', code: 3 });
    });
    expect(written()).toContain('[process exited with code 3]');
    await userEvent.setup().click(screen.getByRole('button', { name: 'Reconnect' }));
    expect(FakeSocket.all).toHaveLength(2);
  });

  it('reports a lost connection without reconnecting by itself', () => {
    vi.useFakeTimers();
    mount();
    act(() => {
      lastSocket().open();
      lastSocket().drop();
    });
    expect(written()).toContain('[connection lost]');
    expect(screen.getByRole('button', { name: 'Reconnect' })).toBeTruthy();
    act(() => void vi.advanceTimersByTime(5000));
    expect(FakeSocket.all).toHaveLength(1);
  });

  it('shows the idle warning until the user types', () => {
    mount();
    act(() => {
      lastSocket().open();
      lastSocket().text({ type: 'idle_warning', seconds: 60 });
    });
    expect(screen.getByText('Closing idle session in 60 s, press any key to keep it open')).toBeTruthy();
    act(() => lastTerm().emitInput('x'));
    expect(screen.queryByText(/Closing idle session/)).toBeNull();
  });

  it('writes [closed: idle] when the server closes an idle session', () => {
    mount();
    act(() => {
      lastSocket().open();
      lastSocket().text({ type: 'closed', reason: 'idle' });
    });
    expect(written()).toContain('[closed: idle]');
  });

  it('shows an error frame in a danger banner', () => {
    mount();
    act(() => {
      lastSocket().open();
      lastSocket().text({ type: 'error', message: 'no shell found in the container' });
    });
    expect(screen.getByRole('alert').textContent).toContain('no shell found in the container');
  });

  it('closes the socket on unmount', () => {
    const { unmount } = mount();
    unmount();
    expect(lastSocket().closed).toBe(true);
    expect(lastTerm().disposed).toBe(true);
  });

  it('says a peer may need a newer Stackstead when the socket drops before opening', () => {
    mount({ scope: peerScope });
    act(() => lastSocket().drop());
    expect(screen.getByText('Could not open a terminal. This server may need a newer Stackstead.')).toBeTruthy();
  });

  it('says it could not open a terminal locally when the socket drops before opening', () => {
    mount();
    act(() => lastSocket().drop());
    expect(screen.getByText('Could not open a terminal.')).toBeTruthy();
    expect(screen.queryByText(/newer Stackstead/)).toBeNull();
  });

  it('does not show the pre-open failure once the socket opened', () => {
    mount({ scope: peerScope });
    act(() => {
      lastSocket().open();
      lastSocket().drop();
    });
    expect(screen.queryByText(/Could not open a terminal/)).toBeNull();
  });
});
