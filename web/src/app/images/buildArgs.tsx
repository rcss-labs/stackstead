import { ListEditor } from '../components/ListEditor';

export interface ArgRow {
  name: string;
  value: string;
}

export const argsToRows = (args: Record<string, string>): ArgRow[] => Object.entries(args).map(([name, value]) => ({ name, value }));

/** Rows without a name are dropped; a repeated name keeps its last value. */
export function rowsToArgs(rows: ArgRow[]): Record<string, string> {
  const args: Record<string, string> = {};
  for (const row of rows) {
    const name = row.name.trim();
    if (name) args[name] = row.value;
  }
  return args;
}

/** Name and value inputs per build argument. Values are plain text, never secrets. */
export function BuildArgsEditor({ rows, onChange, title = 'Build args' }: { rows: ArgRow[]; onChange: (rows: ArgRow[]) => void; title?: string }) {
  return (
    <ListEditor
      title={title}
      items={rows}
      onChange={onChange}
      addLabel="Add build arg"
      newItem={() => ({ name: '', value: '' })}
      row={(item, i, update) => (
        <>
          <input aria-label={`Build arg ${i + 1} name`} placeholder="NAME" spellCheck={false} value={item.name} onChange={(e) => update({ ...item, name: e.target.value })} />
          <input aria-label={`Build arg ${i + 1} value`} placeholder="value" spellCheck={false} value={item.value} onChange={(e) => update({ ...item, value: e.target.value })} />
        </>
      )}
    />
  );
}
