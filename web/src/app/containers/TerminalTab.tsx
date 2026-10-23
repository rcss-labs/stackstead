import { FitAddon } from '@xterm/addon-fit';
import { Terminal } from '@xterm/xterm';
import '@xterm/xterm/css/xterm.css';
import { useEffect, useRef, useState } from 'react';
import { execUrl, explainRefusal, openExec } from '$lib/exec/exec';
import type { ExecEnd, RefusalReason } from '$lib/exec/exec';
import { debounce } from '$lib/stores/debounce';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { EmptyState } from '../components/EmptyState';
import { Field } from '../components/Field';
import { useServer } from '../servers/ServerContext';
import styles from './TerminalTab.module.css';

type Shell = 'auto' | 'bash' | 'sh' | 'custom';

interface Prefs {
  shell: Shell;
  command: string;
  user: string;
}

const DEFAULT_PREFS: Prefs = { shell: 'auto', command: '', user: '' };
const SHELL_PATHS: Partial<Record<Shell, string>> = { bash: '/bin/bash', sh: '/bin/sh' };
const RESIZE_DEBOUNCE_MS = 150;

const storageKey = (name: string) => `stackstead.terminal.${name}`;

// Storage can be missing or throw (private windows, blocked site data); the terminal works without it.
function loadPrefs(name: string): Prefs {
  try {
    const raw = localStorage.getItem(storageKey(name));
    if (raw) return { ...DEFAULT_PREFS, ...(JSON.parse(raw) as Partial<Prefs>) };
  } catch {
    // fall through to the defaults
  }
  return DEFAULT_PREFS;
}

function savePrefs(name: string, prefs: Prefs) {
  try {
    localStorage.setItem(storageKey(name), JSON.stringify(prefs));
  } catch {
    // not persisted
  }
}

function commandFor(prefs: Prefs): string[] | undefined {
  if (prefs.shell === 'custom') {
    const words = prefs.command.trim().split(/\s+/).filter(Boolean);
    return words.length > 0 ? words : undefined;
  }
  const path = SHELL_PATHS[prefs.shell];
  return path ? [path] : undefined;
}

/** The xterm colours follow the app's light and dark tokens. */
function terminalTheme(el: HTMLElement) {
  const css = getComputedStyle(el);
  const token = (name: string, fallback: string) => css.getPropertyValue(name).trim() || fallback;
  return {
    background: token('--bg', '#f3f5f7'),
    foreground: token('--text', '#161a20'),
    cursor: token('--accent', '#047857'),
    selectionBackground: token('--accent-soft', 'rgba(4, 120, 87, 0.25)'),
  };
}

function endNote(end: ExecEnd): string | undefined {
  switch (end.kind) {
    case 'exit':
      return `[process exited with code ${end.code}]`;
    case 'closed':
      return `[closed: ${end.reason.replace('_', ' ')}]`;
    case 'lost':
      return '[connection lost]';
    default:
      return undefined;
  }
}

const REFUSAL_TEXT: Record<Exclude<RefusalReason, 'not_running' | 'peer_old' | 'unknown'>, string> = {
  disabled: 'Terminal is disabled on this server.',
  limit: 'Too many open terminals on this server. Close one and try again.',
  bad_origin: "The server refused the connection's origin. Check the reverse proxy forwards the Host header with its port.",
};

export interface TerminalTabProps {
  containerId: string;
  containerName: string;
  running: boolean;
  onStart(): void;
}

export function TerminalTab({ containerId, containerName, running, onStart }: TerminalTabProps) {
  const { local, id: serverId } = useServer();
  const base = local ? '' : `/api/servers/${encodeURIComponent(serverId)}`;

  const [prefs, setPrefs] = useState(() => loadPrefs(containerName));
  // A session starts with the choices as they were when it was requested, not on every keystroke.
  const [run, setRun] = useState(() => ({ n: 0, prefs }));
  const [end, setEnd] = useState<ExecEnd | undefined>();
  const [idleSeconds, setIdleSeconds] = useState<number | undefined>();
  const [refusal, setRefusal] = useState<RefusalReason | undefined>();
  const hostRef = useRef<HTMLDivElement>(null);

  function update(change: Partial<Prefs>) {
    const next = { ...prefs, ...change };
    setPrefs(next);
    savePrefs(containerName, next);
  }

  function restart() {
    setEnd(undefined);
    setIdleSeconds(undefined);
    setRefusal(undefined);
    setRun((r) => ({ n: r.n + 1, prefs }));
  }

  useEffect(() => {
    const host = hostRef.current;
    if (!running || !host) return;
    const term = new Terminal({
      cursorBlink: true,
      fontFamily: getComputedStyle(host).getPropertyValue('--mono').trim() || 'monospace',
      fontSize: 13,
      theme: terminalTheme(host),
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.open(host);
    fit.fit();

    let opened = false;
    let disposed = false;
    const url = execUrl(base, containerId, { cmd: commandFor(run.prefs), user: run.prefs.user, cols: term.cols, rows: term.rows });
    const handle = openExec(
      url,
      {
        onOpen: () => {
          opened = true;
        },
        onData: (bytes) => {
          term.write(bytes);
          setIdleSeconds(undefined);
        },
        onIdleWarning: setIdleSeconds,
        onEnd: (e) => {
          setEnd(e);
          setIdleSeconds(undefined);
          if (e.kind === 'lost' && !opened) {
            // The browser hides why an upgrade was refused; ask the server over plain HTTP.
            void explainRefusal(url, !local).then((reason) => {
              if (!disposed) setRefusal(reason);
            });
          } else {
            const note = endNote(e);
            if (note) term.write(`\r\n${note}\r\n`);
          }
        },
      },
    );
    const input = term.onData((data) => {
      handle.send(data);
      setIdleSeconds(undefined);
    });

    const sendSize = debounce(() => handle.resize(term.cols, term.rows), RESIZE_DEBOUNCE_MS);
    const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(() => {
      fit.fit();
      sendSize.trigger();
    }) : undefined;
    observer?.observe(host);

    // The light/dark switch changes the tokens the terminal colours come from.
    const restyle = () => {
      term.options.theme = terminalTheme(host);
    };
    const mq = typeof matchMedia === 'function' ? matchMedia('(prefers-color-scheme: dark)') : undefined;
    mq?.addEventListener('change', restyle);
    const mutations = typeof MutationObserver === 'function' ? new MutationObserver(restyle) : undefined;
    mutations?.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] });
    term.focus();

    return () => {
      sendSize.cancel();
      observer?.disconnect();
      mq?.removeEventListener('change', restyle);
      mutations?.disconnect();
      disposed = true;
      input.dispose();
      handle.close();
      term.dispose();
    };
  }, [running, containerId, base, run, local]);

  if (!running || refusal === 'not_running') {
    return (
      <EmptyState title="Container isn't running">
        <Button variant="primary" onClick={onStart}>
          Start
        </Button>
      </EmptyState>
    );
  }

  return (
    <div className={styles.tab}>
      <div className={styles.controls}>
        <Field label="Shell" id="terminal-shell">
          {({ readOnly: _, ...c }) => (
            <select {...c} value={prefs.shell} onChange={(e) => update({ shell: e.target.value as Shell })}>
              <option value="auto">Auto</option>
              <option value="bash">bash</option>
              <option value="sh">sh</option>
              <option value="custom">Custom command</option>
            </select>
          )}
        </Field>
        {prefs.shell === 'custom' && (
          <Field label="Command" id="terminal-command" mono>
            {({ readOnly: _, ...c }) => <input {...c} value={prefs.command} placeholder="python -i" onChange={(e) => update({ command: e.target.value })} />}
          </Field>
        )}
        <Field label="User" id="terminal-user" mono>
          {({ readOnly: _, ...c }) => <input {...c} value={prefs.user} placeholder="default" onChange={(e) => update({ user: e.target.value })} />}
        </Field>
        <Button variant="secondary" onClick={restart}>
          {end ? 'Reconnect' : 'Apply and restart'}
        </Button>
      </div>
      {refusal && (
        <Banner
          kind="danger"
          title={
            refusal === 'peer_old'
              ? 'Could not open a terminal. This server may need a newer Stackstead.'
              : refusal === 'unknown'
                ? 'Could not open a terminal.'
                : REFUSAL_TEXT[refusal]
          }
        />
      )}
      {end?.kind === 'error' && (
        <Banner kind="danger" title="Terminal error">
          {end.message}
        </Banner>
      )}
      {idleSeconds !== undefined && <Banner kind="warning" title={`Closing idle session in ${idleSeconds} s, press any key to keep it open`} />}
      <div ref={hostRef} className={styles.terminal} />
    </div>
  );
}

export default TerminalTab;
