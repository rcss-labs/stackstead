// The only module that imports CodeMirror. Components load it with a dynamic
// import() so the editor lands in its own chunk and the stack list stays small.
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { dockerFile } from '@codemirror/legacy-modes/mode/dockerfile';
import { StreamLanguage } from '@codemirror/language';
import { yaml } from '@codemirror/lang-yaml';
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { unifiedMergeView } from '@codemirror/merge';
import { Compartment, EditorState, type Extension, RangeSet, StateEffect, StateField } from '@codemirror/state';
import { Decoration, type DecorationSet, EditorView, GutterMarker, gutter, keymap, lineNumbers, ViewPlugin, type ViewUpdate } from '@codemirror/view';
import { tags } from '@lezer/highlight';
import { changedLines } from './changedLines';

export interface EditorOptions {
  parent: HTMLElement;
  value: string;
  readonly: boolean;
  ariaLabel: string;
  /** Syntax mode; defaults to YAML. */
  language?: 'yaml' | 'dockerfile';
  /** Baseline for changed-line marks; none are drawn while it is undefined. */
  original?: string;
  onchange: (value: string) => void;
}

export interface EditorHandle {
  getValue(): string;
  setValue(value: string): void;
  setReadonly(readonly: boolean): void;
  setOriginal(original: string | undefined): void;
  goToLine(line: number): void;
  focus(): void;
  destroy(): void;
}

const readonlyExtensions = (readonly: boolean): Extension[] => [
  EditorState.readOnly.of(readonly),
  EditorView.editable.of(!readonly),
];

const highlightStyle = HighlightStyle.define([
  { tag: [tags.propertyName, tags.definition(tags.propertyName)], color: 'var(--info)' },
  // YAML plain scalars are all `tags.content`; numbers cannot be told apart from other
  // plain values, so spec 6.4's "numbers --warning" is not applied.
  { tag: [tags.string, tags.special(tags.string), tags.content], color: 'var(--success)' },
  { tag: [tags.comment, tags.lineComment], color: 'var(--faint)' },
  { tag: tags.keyword, color: 'var(--info)' },
]);

function languageExtension(language: 'yaml' | 'dockerfile'): Extension {
  // The legacy mode has no name, which would leave .cm-content without data-language; the name is set here.
  return language === 'dockerfile' ? StreamLanguage.define({ ...dockerFile, name: 'dockerfile' }) : yaml();
}

function baseExtensions(ariaLabel: string, language: 'yaml' | 'dockerfile'): Extension[] {
  return [
    lineNumbers(),
    languageExtension(language),
    syntaxHighlighting(highlightStyle),
    EditorView.lineWrapping,
    EditorView.contentAttributes.of({ 'aria-label': ariaLabel }),
    EditorView.theme({
      '&': { background: 'var(--surface)', color: 'var(--text)' },
      '.cm-content': { fontFamily: 'var(--mono)', caretColor: 'var(--text)' },
      '.cm-gutters': { background: 'var(--surface)', color: 'var(--faint)', border: 'none' },
      '.cm-activeLine, .cm-activeLineGutter': { background: 'transparent' },
      '&.cm-focused': { outline: '2px solid var(--accent)', outlineOffset: '-2px' },
    }),
  ];
}

// --- Changed-line marks: line background + gutter marker, 150 ms after the last change ---

const CHANGED_DEBOUNCE_MS = 150;

const setOriginalEffect = StateEffect.define<string | undefined>();
const setChangedEffect = StateEffect.define<number[]>();

const originalField = StateField.define<string | undefined>({
  create: () => undefined,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setOriginalEffect)) return e.value;
    return value;
  },
});

class ChangedMarker extends GutterMarker {
  toDOM(): Node {
    const el = document.createElement('span');
    el.className = 'cm-changed-marker';
    return el;
  }
}
const changedMarker = new ChangedMarker();
const changedLineDeco = Decoration.line({ class: 'cm-changed-line' });

interface ChangedState {
  lines: DecorationSet;
  markers: RangeSet<GutterMarker>;
}

const changedField = StateField.define<ChangedState>({
  create: () => ({ lines: Decoration.none, markers: RangeSet.empty }),
  update(value, tr) {
    for (const e of tr.effects) {
      if (!e.is(setChangedEffect)) continue;
      const starts = e.value.filter((n) => n >= 1 && n <= tr.state.doc.lines).map((n) => tr.state.doc.line(n).from);
      return {
        lines: Decoration.set(starts.map((from) => changedLineDeco.range(from))),
        markers: RangeSet.of(starts.map((from) => changedMarker.range(from))),
      };
    }
    if (!tr.docChanged) return value;
    return { lines: value.lines.map(tr.changes), markers: value.markers.map(tr.changes) };
  },
  provide: (f) => EditorView.decorations.from(f, (v) => v.lines),
});

const changedScheduler = ViewPlugin.fromClass(
  class {
    timer: ReturnType<typeof setTimeout> | undefined;
    constructor(readonly view: EditorView) {}
    update(u: ViewUpdate) {
      const baselineChanged = u.transactions.some((t) => t.effects.some((e) => e.is(setOriginalEffect)));
      if (!u.docChanged && !baselineChanged) return;
      clearTimeout(this.timer);
      this.timer = setTimeout(() => {
        const original = this.view.state.field(originalField);
        const lines = original === undefined ? [] : changedLines(original, this.view.state.doc.toString());
        this.view.dispatch({ effects: setChangedEffect.of(lines) });
      }, CHANGED_DEBOUNCE_MS);
    }
    destroy() {
      clearTimeout(this.timer);
    }
  },
);

const changedLinesExtensions: Extension[] = [
  changedField,
  changedScheduler,
  gutter({ class: 'cm-changed-gutter', markers: (v) => v.state.field(changedField).markers }),
  EditorView.theme({
    '.cm-changed-line': { background: 'var(--success-soft)' },
    '.cm-changed-gutter': { width: '4px' },
    '.cm-changed-marker': { display: 'block', width: '3px', height: '100%', background: 'var(--success)' },
  }),
];

export function createEditor(opts: EditorOptions): EditorHandle {
  const readonlyCompartment = new Compartment();
  const view = new EditorView({
    parent: opts.parent,
    state: EditorState.create({
      doc: opts.value,
      extensions: [
        ...baseExtensions(opts.ariaLabel, opts.language ?? 'yaml'),
        ...changedLinesExtensions,
        originalField.init(() => opts.original),
        readonlyCompartment.of(readonlyExtensions(opts.readonly)),
        history(),
        keymap.of([...defaultKeymap, ...historyKeymap]),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) opts.onchange(u.state.doc.toString());
        }),
      ],
    }),
  });
  // Draw the marks for a draft that already differs from its baseline (e.g. a kept draft after a conflict).
  if (opts.original !== undefined && opts.original !== opts.value) {
    view.dispatch({ effects: setOriginalEffect.of(opts.original) });
  }
  return {
    getValue: () => view.state.doc.toString(),
    setValue(value) {
      const current = view.state.doc.toString();
      if (value === current) return;
      view.dispatch({ changes: { from: 0, to: current.length, insert: value } });
    },
    setReadonly(readonly) {
      view.dispatch({ effects: readonlyCompartment.reconfigure(readonlyExtensions(readonly)) });
    },
    setOriginal(original) {
      if (original === view.state.field(originalField)) return;
      view.dispatch({ effects: setOriginalEffect.of(original) });
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
  language?: 'yaml' | 'dockerfile';
}

/** Read-only unified diff; unchanged stretches collapse to 3 lines of context. */
export function createDiffView(opts: DiffOptions): { destroy(): void } {
  const view = new EditorView({
    parent: opts.parent,
    state: EditorState.create({
      doc: opts.modified,
      extensions: [
        ...baseExtensions(opts.ariaLabel, opts.language ?? 'yaml'),
        ...readonlyExtensions(true),
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
