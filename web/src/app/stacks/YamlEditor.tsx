import { useEffect, useImperativeHandle, useRef, useState, type Ref } from 'react';
import type { EditorHandle } from '$lib/stacks/cm';
import styles from './YamlEditor.module.css';

export interface YamlEditorHandle {
  /** 1-based line; best effort. */
  goToLine(line: number): void;
  focus(): void;
}

export interface YamlEditorProps {
  value: string;
  readonly?: boolean;
  ariaLabel: string;
  /** Baseline for the changed-line marks; none are drawn when absent. */
  original?: string;
  onChange?: (value: string) => void;
  ref?: Ref<YamlEditorHandle>;
}

// Expects a controlled parent: `value` echoes what `onChange` reported (the editor itself is uncontrolled).
export function YamlEditor({ value, readonly = false, ariaLabel, original, onChange, ref }: YamlEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const handleRef = useRef<EditorHandle | undefined>(undefined);
  const [ready, setReady] = useState(false);

  // The editor is created once; it reads the freshest props through this ref.
  const latest = useRef({ value, readonly, ariaLabel, original, onChange });
  latest.current = { value, readonly, ariaLabel, original, onChange };

  useEffect(() => {
    let destroyed = false;
    // Dynamic import: CodeMirror is its own chunk. Until it arrives (or if it
    // fails to load) the textarea is a fully working editor.
    import('$lib/stacks/cm')
      .then(({ createEditor }) => {
        const host = hostRef.current;
        if (destroyed || !host) return;
        const textarea = textareaRef.current;
        const hadFocus = textarea !== null && document.activeElement === textarea;
        const now = latest.current;
        // The textarea may hold keystrokes the parent has not echoed back yet.
        const text = textarea ? textarea.value : now.value;
        const handle = createEditor({
          parent: host,
          value: text,
          readonly: now.readonly,
          ariaLabel: now.ariaLabel,
          original: now.original,
          onchange: (v) => latest.current.onChange?.(v),
        });
        handleRef.current = handle;
        setReady(true);
        if (hadFocus) handle.focus(); // the textarea is about to be removed; do not drop the user's focus
      })
      .catch(() => {});
    return () => {
      destroyed = true;
      handleRef.current?.destroy();
      handleRef.current = undefined;
      setReady(false);
    };
  }, []);

  // Keep the active editor (CodeMirror or textarea) in step with value changes made by the parent (reload, discard).
  useEffect(() => {
    const handle = handleRef.current;
    if (handle) {
      if (handle.getValue() !== value) handle.setValue(value);
    } else if (textareaRef.current && textareaRef.current.value !== value) {
      textareaRef.current.value = value;
    }
  }, [value, ready]);

  useEffect(() => {
    handleRef.current?.setOriginal(original);
  }, [original, ready]);

  useEffect(() => {
    handleRef.current?.setReadonly(readonly);
  }, [readonly, ready]);

  useImperativeHandle(
    ref,
    () => ({
      goToLine(line: number) {
        if (handleRef.current) return handleRef.current.goToLine(line);
        const textarea = textareaRef.current;
        if (!textarea) return;
        const lines = textarea.value.split('\n');
        const n = Math.min(Math.max(1, line), lines.length);
        const pos = lines.slice(0, n - 1).reduce((acc, l) => acc + l.length + 1, 0);
        textarea.focus();
        textarea.setSelectionRange(pos, pos);
      },
      focus() {
        if (handleRef.current) handleRef.current.focus();
        else textareaRef.current?.focus();
      },
    }),
    [],
  );

  return (
    <div className={styles.editor}>
      <div ref={hostRef} />
      {!ready && (
        <textarea
          ref={textareaRef}
          className={styles.fallback}
          aria-label={ariaLabel}
          readOnly={readonly}
          spellCheck={false}
          rows={20}
          defaultValue={value}
          onChange={(e) => onChange?.(e.currentTarget.value)}
        />
      )}
    </div>
  );
}
