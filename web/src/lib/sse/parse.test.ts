import { describe, expect, it } from 'vitest';
import { createParser, type SSEMessage } from './parse';

const SAMPLE = ': hello\nid: 7\nevent: change\ndata: {"a":1}\n\ndata: x\ndata: y\n\n: ping\nretry: 500\ndata: z\n\n';

function parseAll(chunks: string[]) {
  const p = createParser();
  const messages: SSEMessage[] = [];
  let comments = 0;
  for (const c of chunks) {
    const r = p.feed(c);
    messages.push(...r.messages);
    comments += r.comments;
  }
  messages.push(...p.flush());
  return { messages, comments };
}

const EXPECTED: SSEMessage[] = [
  { event: 'change', data: '{"a":1}', id: '7' },
  { event: 'message', data: 'x\ny' },
  { event: 'message', data: 'z', retry: 500 },
];

describe('createParser', () => {
  it('parses the whole sample', () => {
    const r = parseAll([SAMPLE]);
    expect(r.messages).toEqual(EXPECTED);
    expect(r.comments).toBe(2);
  });

  it('gives the same result at every split point', () => {
    for (let i = 0; i <= SAMPLE.length; i++) {
      const r = parseAll([SAMPLE.slice(0, i), SAMPLE.slice(i)]);
      expect(r.messages, `split at ${i}`).toEqual(EXPECTED);
      expect(r.comments, `split at ${i}`).toBe(2);
    }
  });

  it('gives the same result fed one character at a time', () => {
    expect(parseAll([...SAMPLE]).messages).toEqual(EXPECTED);
  });

  it('accepts \\r\\n and bare \\r line ends, including a split \\r\\n', () => {
    const crlf = SAMPLE.replace(/\n/g, '\r\n');
    const cr = SAMPLE.replace(/\n/g, '\r');
    expect(parseAll([crlf]).messages).toEqual(EXPECTED);
    expect(parseAll([cr]).messages).toEqual(EXPECTED);
    for (let i = 0; i <= crlf.length; i++) {
      expect(parseAll([crlf.slice(0, i), crlf.slice(i)]).messages, `crlf split ${i}`).toEqual(EXPECTED);
    }
  });

  it('does not dispatch a block without data', () => {
    // event-only and id-only blocks are dropped; a bare `data` field is present-but-empty and dispatches.
    expect(parseAll(['event: x\n\n', 'id: 3\n\n', 'data\n\n']).messages).toEqual([{ event: 'message', data: '' }]);
  });

  it('keeps a value with a leading space stripped once and no colon as empty value', () => {
    expect(parseAll(['data:  two\n\n']).messages).toEqual([{ event: 'message', data: ' two' }]);
  });

  it('reports an id only on the messages that carry one', () => {
    const r = parseAll(['id: 1\ndata: a\n\ndata: b\n\nid: 2\ndata: c\n\n']);
    expect(r.messages.map((m) => m.id)).toEqual(['1', undefined, '2']);
  });

  it('ignores a non-numeric retry and an unterminated trailing block on flush', () => {
    const r = parseAll(['retry: abc\ndata: a\n\ndata: unfinished']);
    expect(r.messages).toEqual([{ event: 'message', data: 'a' }]);
  });
});
