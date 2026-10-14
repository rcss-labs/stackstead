// Pure Server-Sent Events line parser (WHATWG HTML §9.2.6). It never touches
// the network, so every split point of a stream can be tested directly.

export interface SSEMessage {
  event: string;
  data: string;
  /** Only present on messages that carried an `id:` field; the client keeps the last one. */
  id?: string;
  retry?: number;
}

export interface Parser {
  feed(chunk: string): { messages: SSEMessage[]; comments: number };
  /** Called when the stream ends: finishes a trailing line but drops an unterminated block. */
  flush(): SSEMessage[];
}

export function createParser(): Parser {
  let buffer = '';
  let atStart = true;
  let event = '';
  let data: string[] | undefined;
  let id: string | undefined;
  let retry: number | undefined;

  function dispatch(out: SSEMessage[]) {
    if (data !== undefined) {
      const m: SSEMessage = { event: event || 'message', data: data.join('\n') };
      if (id !== undefined) m.id = id;
      if (retry !== undefined) m.retry = retry;
      out.push(m);
    }
    event = '';
    data = undefined;
    id = undefined;
    retry = undefined;
  }

  function line(l: string, out: SSEMessage[]): number {
    if (l === '') {
      dispatch(out);
      return 0;
    }
    if (l.startsWith(':')) return 1;
    const colon = l.indexOf(':');
    const field = colon === -1 ? l : l.slice(0, colon);
    let value = colon === -1 ? '' : l.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    switch (field) {
      case 'event':
        event = value;
        break;
      case 'data':
        (data ??= []).push(value);
        break;
      case 'id':
        if (!value.includes('\0')) id = value;
        break;
      case 'retry':
        if (/^\d+$/.test(value)) retry = Number(value);
        break;
    }
    return 0;
  }

  function drain(final: boolean) {
    const messages: SSEMessage[] = [];
    let comments = 0;
    let pos = 0;
    for (;;) {
      let end = -1;
      for (let i = pos; i < buffer.length; i++) {
        const ch = buffer[i];
        if (ch === '\n') {
          end = i;
          break;
        }
        if (ch === '\r') {
          // A trailing \r may be the first half of \r\n: wait for the next chunk.
          if (i === buffer.length - 1 && !final) break;
          end = i;
          break;
        }
      }
      if (end === -1) break;
      comments += line(buffer.slice(pos, end), messages);
      pos = buffer[end] === '\r' && buffer[end + 1] === '\n' ? end + 2 : end + 1;
    }
    buffer = buffer.slice(pos);
    return { messages, comments };
  }

  return {
    feed(chunk) {
      if (atStart && chunk.length > 0) {
        atStart = false;
        if (chunk.charCodeAt(0) === 0xfeff) chunk = chunk.slice(1);
      }
      buffer += chunk;
      return drain(false);
    },
    flush() {
      const r = drain(true);
      if (buffer !== '') {
        const extra: SSEMessage[] = [];
        line(buffer, extra);
        r.messages.push(...extra);
        buffer = '';
      }
      // An unterminated block (no blank line) is discarded, per the spec.
      event = '';
      data = undefined;
      id = undefined;
      retry = undefined;
      return r.messages;
    },
  };
}
