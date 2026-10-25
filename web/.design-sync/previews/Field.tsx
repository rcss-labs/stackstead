import { Field } from 'stackstead-ui';

const stack = { display: 'grid', gap: 16, maxWidth: 360 };

export function WithHint() {
  return (
    <div style={stack}>
      <Field label="Keep last" id="keep-last" hint="Versions to keep per image; 0 turns this rule off.">
        {({ readOnly: _, ...c }) => <input {...c} inputMode="numeric" defaultValue="5" />}
      </Field>
    </div>
  );
}

export function WithError() {
  return (
    <div style={stack}>
      <Field label="Keep days" id="keep-days" hint="Keep versions built within this many days; 0 turns this rule off." error="Enter a whole number from 0 to 365">
        {({ readOnly: _, ...c }) => <input {...c} inputMode="numeric" defaultValue="-3" />}
      </Field>
    </div>
  );
}

export function ReadonlyMono() {
  return (
    <div style={stack}>
      <Field label="Metrics token" id="metrics-token" mono readonly readonlyReason="Rotate the token to change it.">
        {(c) => <input {...c} type="text" defaultValue="sst_7f3a9c1e42b84d0d" autoComplete="off" />}
      </Field>
    </div>
  );
}

export function Select() {
  return (
    <div style={stack}>
      <Field label="Restart policy" id="restart-policy" hint="Applied when the stack is next deployed.">
        {({ readOnly, ...c }) => (
          <select {...c} disabled={readOnly} defaultValue="unless-stopped">
            <option value="no">no</option>
            <option value="always">always</option>
            <option value="unless-stopped">unless-stopped</option>
            <option value="on-failure">on-failure</option>
          </select>
        )}
      </Field>
    </div>
  );
}
