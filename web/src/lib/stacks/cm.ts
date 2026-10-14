// The only module that imports CodeMirror. Components load it with a dynamic
// import() so the editor lands in its own chunk and the stack list stays small.
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { yaml } from '@codemirror/lang-yaml';
import { defaultHighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { unifiedMergeView } from '@codemirror/merge';
import { EditorState, type Extension } from '@codemirror/state';
import { EditorView, keymap, lineNumbers } from '@codemirror/view';

export interface EditorOptions {
  parent: HTMLElement;
  value: string;
  readonly: boolean;
  ariaLabel: string;
  onchange: (value: string) => void;
}

export interface EditorHandle {
  getValue(): string;
  setValue(value: string): void;
  goToLine(line: number): void;
  focus(): void;
  destroy(): void;
}

function baseExtensions(ariaLabel: string, readonly: boolean): Extension[] {
  return [
    lineNumbers(),
    yaml(),
    syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
    EditorView.lineWrapping,
    EditorView.contentAttributes.of({ 'aria-label': ariaLabel }),
    EditorState.readOnly.of(readonly),
    EditorView.editable.of(!readonly),
    EditorView.theme({
      '&': { border: '1px solid var(--border)', borderRadius: 'var(--radius-md)', background: 'var(--surface)', color: 'var(--text)' },
      '.cm-content': { fontFamily: 'var(--font-mono)' },
      '.cm-gutters': { background: 'var(--surface-raised)', color: 'var(--muted)', border: 'none' },
      '&.cm-focused': { outline: '2px solid var(--accent)' },
    }),
  ];
}

export function createEditor(opts: EditorOptions): EditorHandle {
  const view = new EditorView({
    parent: opts.parent,
    state: EditorState.create({
      doc: opts.value,
      extensions: [
        ...baseExtensions(opts.ariaLabel, opts.readonly),
        history(),
        keymap.of([...defaultKeymap, ...historyKeymap]),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) opts.onchange(u.state.doc.toString());
        }),
      ],
    }),
  });
  return {
    getValue: () => view.state.doc.toString(),
    setValue(value) {
      const current = view.state.doc.toString();
      if (value === current) return;
      view.dispatch({ changes: { from: 0, to: current.length, insert: value } });
    },
    goToLine(line) {
      const n = Math.min(Math.max(1, line), view.state.doc.lines);
      const pos = view.state.doc.line(n).from;
      view.dispatch({ selection: { anchor: pos }, scrollIntoView: true });
      view.focus();
    },
    focus: () => view.focus(),
    destroy: () => view.destroy(),
  };
}

export interface DiffOptions {
  parent: HTMLElement;
  original: string;
  modified: string;
  ariaLabel: string;
}

/** Read-only unified diff; unchanged stretches collapse to 3 lines of context. */
export function createDiffView(opts: DiffOptions): { destroy(): void } {
  const view = new EditorView({
    parent: opts.parent,
    state: EditorState.create({
      doc: opts.modified,
      extensions: [
        ...baseExtensions(opts.ariaLabel, true),
        unifiedMergeView({
          original: opts.original,
          mergeControls: false,
          collapseUnchanged: { margin: 3, minSize: 4 },
        }),
      ],
    }),
  });
  return { destroy: () => view.destroy() };
}
