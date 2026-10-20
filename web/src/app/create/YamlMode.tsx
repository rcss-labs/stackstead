import { useRef } from 'react';
import { Button } from '../components/Button';
import { YamlEditor, type YamlEditorHandle } from '../stacks/YamlEditor';
import type { YamlError } from './useCreateForm';
import styles from './YamlMode.module.css';

export interface YamlModeProps {
  text: string;
  onChange: (text: string) => void;
  error: YamlError | null;
  /** Server and form checks, one line per problem: "{service}: {field}: {message}". */
  fieldErrors: string[];
  disabled: boolean;
}

/** The whole stack as one compose document, with the parse error and the checks that came back from Create. */
export function YamlMode({ text, onChange, error, fieldErrors, disabled }: YamlModeProps) {
  const editor = useRef<YamlEditorHandle>(null);
  const { line } = error ?? {};
  return (
    <div className={styles.yaml}>
      {fieldErrors.length > 0 && (
        <ul id="yaml-field-errors" data-testid="yaml-field-errors" className={styles.fieldErrors} role="alert" tabIndex={-1}>
          {fieldErrors.map((message, index) => (
            <li key={`${index}-${message}`}>{message}</li>
          ))}
        </ul>
      )}
      <YamlEditor ref={editor} ariaLabel="Stack compose file" value={text} readonly={disabled} onChange={onChange} invalid={error !== null} describedBy={error ? 'yaml-error' : undefined} />
      {error && (
        <div className={styles.error} aria-live="polite">
          <span id="yaml-error" data-testid="yaml-error">
            {line !== undefined ? `Line ${line}: ${error.message}` : error.message}
          </span>
          {line !== undefined && (
            <Button variant="secondary" size="sm" onClick={() => editor.current?.goToLine(line)}>
              Go to line
            </Button>
          )}
        </div>
      )}
    </div>
  );
}
