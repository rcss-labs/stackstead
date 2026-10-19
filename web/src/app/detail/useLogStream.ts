import { useCallback, useEffect, useRef, useState } from 'react';
import { readable } from '$lib/store';
import type { LogError } from '$lib/api/types';
import type { LogLine } from '$lib/logs/buffer';
import { createLogStream, type LogSource, type LogStream, type LogStreamOptions } from '$lib/stacks/logs';
import type { SSEStatus } from '$lib/sse/client';
import { useStore } from '../hooks/useStore';

// Stand-ins used until the effect has opened the stream, so the hook always reads real stores.
const NO_LINES = readable<LogLine[]>([]);
const NO_ERRORS = readable<LogError[]>([]);
const CONNECTING = readable<SSEStatus>('connecting');
const NOT_ENDED = readable(false);
const NONE_DROPPED = readable(0);

export interface LogStreamState {
  lines: LogLine[];
  status: SSEStatus;
  errors: LogError[];
  ended: boolean;
  dropped: number;
  restart: (opts: LogStreamOptions) => void;
  clear: () => void;
}

/**
 * One log stream for the lifetime of the mounted component. It is opened in an effect and closed in that
 * effect's cleanup, so StrictMode's mount / unmount / mount ends with exactly one live request. `initial`
 * is read once per source; later option changes go through `restart`.
 */
export function useLogStream(source: LogSource, initial: LogStreamOptions): LogStreamState {
  const [stream, setStream] = useState<LogStream | undefined>();
  const initialRef = useRef(initial);
  initialRef.current = initial;
  const sourceKey = source.kind === 'stack' ? `stack:${source.name}` : `container:${source.id}`;
  const sourceRef = useRef(source);
  sourceRef.current = source;

  useEffect(() => {
    const opened = createLogStream(sourceRef.current, initialRef.current);
    setStream(opened);
    return () => {
      opened.close();
      setStream(undefined);
    };
  }, [sourceKey]);

  const lines = useStore(stream?.buffer ?? NO_LINES);
  const status = useStore(stream?.status ?? CONNECTING);
  const errors = useStore(stream?.errors ?? NO_ERRORS);
  const ended = useStore(stream?.ended ?? NOT_ENDED);
  const dropped = useStore(stream?.dropped ?? NONE_DROPPED);

  const restart = useCallback((opts: LogStreamOptions) => stream?.restart(opts), [stream]);
  const clear = useCallback(() => stream?.clear(), [stream]);
  return { lines, status, errors, ended, dropped, restart, clear };
}
