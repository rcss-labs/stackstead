import { useId, useImperativeHandle, useRef, useState } from 'react';
import type { ClipboardEvent, ReactNode, Ref } from 'react';
import type { Field as FieldGroup } from '$lib/api/types';
import type { FormErrors, ServiceForm as ServiceFormValue } from '$lib/compose/form';
import { Button } from '../components/Button';
import { Field } from '../components/Field';
import { ListEditor } from '../components/ListEditor';
import styles from './ServiceForm.module.css';

export interface ServiceFormHandle {
  focusFirstError(): void;
}

export interface ServiceFormProps {
  form: ServiceFormValue;
  onChange: (form: ServiceFormValue) => void;
  errors: FormErrors;
  showName?: boolean;
  name?: string;
  onNameChange?: (name: string) => void;
  nameReadonly?: boolean;
  onBrowse?: (mode: 'dir' | 'file', set: (path: string) => void) => void;
  networkPicker?: (form: ServiceFormValue, rowErrors: Record<number, string>) => ReactNode;
  onRawEditor?: () => void;
  /** Rendered beside the service name (New stack puts the stack name here). */
  lead?: ReactNode;
  /** Prefix of every control id; give each form on a page its own. */
  idPrefix?: string;
  ref?: Ref<ServiceFormHandle>;
}

const RO_HINT = "This field uses YAML the form can't edit — open the raw YAML editor";
const RESTART: [string, string][] = [
  ['', 'Default (no restart)'],
  ['no', 'no'],
  ['always', 'always'],
  ['unless-stopped', 'unless-stopped'],
  ['on-failure', 'on-failure'],
];
const MODE_PRESETS = ['', 'host', 'none', 'bridge'];

const FIXED_LABELS: Record<string, string> = {
  name: 'Service name',
  'build.context': 'Build context',
  'build.dockerfile': 'Dockerfile',
  networkMode: 'Network mode',
  'limits.cpus': 'CPUs',
  'limits.memory': 'Memory',
  restart: 'Restart policy',
  'healthcheck.test': 'Healthcheck command',
  'healthcheck.interval': 'Healthcheck interval',
  'healthcheck.timeout': 'Healthcheck timeout',
  'healthcheck.startPeriod': 'Healthcheck start period',
  'healthcheck.retries': 'Healthcheck retries',
};

// Human-readable name of a form path, matching the control's own label.
function labelOf(path: string, source: ServiceFormValue['source']): string {
  if (path === 'image') return source === 'build' ? 'Image tag' : 'Image';
  if (FIXED_LABELS[path]) return FIXED_LABELS[path];
  const row = /^(ports|volumes|env|networks)\[(\d+)\](?:\.(name|value|ipv4Address))?$/.exec(path);
  if (!row) return path;
  const n = Number(row[2]) + 1;
  const part = row[3];
  if (row[1] === 'ports') return `Port ${n}`;
  if (row[1] === 'volumes') return `Volume ${n}`;
  if (row[1] === 'env') return `Variable ${n}${part ? ` ${part}` : ''}`;
  return `Network ${n}${part === 'ipv4Address' ? ' IPv4 address' : part ? ` ${part}` : ''}`;
}

function rowErrors(shown: FormErrors, prefix: string, suffixes: string[] = ['']): Record<number, string> {
  const out: Record<number, string> = {};
  for (const [path, message] of Object.entries(shown)) {
    for (const suffix of suffixes) {
      const m = new RegExp(`^${prefix}\\[(\\d+)\\]${suffix.replace('.', '\\.')}$`).exec(path);
      if (m) out[Number(m[1])] = out[Number(m[1])] ? `${out[Number(m[1])]}; ${message}` : message;
    }
  }
  return out;
}

export function ServiceForm({
  form,
  onChange,
  errors,
  showName = false,
  name = '',
  onNameChange,
  nameReadonly = false,
  onBrowse,
  networkPicker,
  onRawEditor,
  lead,
  idPrefix = 'svc',
  ref,
}: ServiceFormProps) {
  const radioName = useId();
  const root = useRef<HTMLDivElement>(null);
  const [pickedCustom, setPickedCustom] = useState(false);
  const customMode = pickedCustom || !MODE_PRESETS.includes(form.networkMode);
  const id = (suffix: string) => `${idPrefix}-${suffix}`;

  useImperativeHandle(ref, () => ({
    focusFirstError() {
      const target = root.current?.querySelector<HTMLElement>('[aria-invalid="true"]') ?? root.current?.querySelector<HTMLElement>('[data-form-error]');
      target?.focus();
    },
  }));

  const ro = (group: FieldGroup) => form.readOnly.has(group);
  const sourceReadOnly = ro('image') || ro('build');
  // Patches build on the latest form, not the one captured by the render that created the handler:
  // the onBrowse callback runs after the file browser closes, when the parent may have moved on.
  const latest = useRef(form);
  latest.current = form;
  const patch = (changes: Partial<ServiceFormValue>) => onChange({ ...latest.current, ...changes });
  const patchHealthcheck = (changes: Partial<ServiceFormValue['healthcheck']>) =>
    onChange({ ...latest.current, healthcheck: { ...latest.current.healthcheck, ...changes } });

  // Errors for the source that is not selected, and for a disabled healthcheck, are not shown or counted.
  const shown: FormErrors = {};
  for (const [path, message] of Object.entries(errors)) {
    if (form.source === 'image' && path.startsWith('build.') && !sourceReadOnly) continue;
    if (path.startsWith('healthcheck.') && !form.healthcheck.enabled && !ro('healthcheck')) continue;
    shown[path] = message;
  }
  const paths = Object.keys(shown);
  const summary =
    paths.length > 0 ? `${paths.length} ${paths.length === 1 ? 'field has an error' : 'fields have errors'}: ${paths.map((p) => labelOf(p, form.source)).join(', ')}` : '';

  const portErrors = rowErrors(shown, 'ports');
  const volumeErrors = rowErrors(shown, 'volumes');
  const envErrors = rowErrors(shown, 'env', ['.name', '.value']);
  const networkErrors = rowErrors(shown, 'networks', ['.name', '.ipv4Address']);
  const invalid = (path: string) => (shown[path] ? ('true' as const) : undefined);

  function selectMode(value: string) {
    if (value === 'custom') {
      setPickedCustom(true);
      patch({ networkMode: '' });
    } else {
      setPickedCustom(false);
      patch({ networkMode: value });
    }
  }

  function pasteEnv(event: ClipboardEvent<HTMLInputElement>, index: number) {
    if (ro('environment')) return;
    const row = form.env[index];
    if (!row || row.name !== '' || row.value !== '') return;
    const pairs: { name: string; value: string }[] = [];
    for (const raw of event.clipboardData.getData('text').split(/\r?\n/)) {
      const line = raw.trim();
      if (line === '' || line.startsWith('#')) continue;
      const eq = line.indexOf('=');
      if (eq <= 0) return; // not an env dump: let the browser paste it as plain text
      pairs.push({ name: line.slice(0, eq).trim(), value: line.slice(eq + 1) });
    }
    if (pairs.length === 0) return;
    event.preventDefault();
    patch({ env: [...form.env.slice(0, index), ...pairs, ...form.env.slice(index + 1)] });
  }

  const readonlyNote = (group: FieldGroup) =>
    ro(group) ? (
      <p className={styles.note} id={id(`ro-${group}`)}>
        {RO_HINT}
        {onRawEditor && (
          <Button variant="secondary" aria-label={`Open raw YAML editor for ${group}`} onClick={onRawEditor}>
            Open raw YAML editor
          </Button>
        )}
      </p>
    ) : null;
  const describedBy = (group: FieldGroup) => (ro(group) ? id(`ro-${group}`) : undefined);

  const textProps = { autoComplete: 'off', spellCheck: false } as const;

  return (
    <div className={styles.form} ref={root}>
      <p className="visually-hidden" role="status">
        {summary}
      </p>
      {errors['_form'] && (
        <div className={styles.formError} role="alert" tabIndex={-1} data-form-error>
          {errors['_form']}
        </div>
      )}

      {showName && (
        <div className={styles.names}>
          {lead}
          <Field label="Service name" id={id('name')} mono error={shown['name']} readonly={nameReadonly} readonlyReason="The name of an existing service cannot be changed here.">
            {(aria) => <input type="text" {...aria} value={name} onChange={(e) => onNameChange?.(e.target.value)} {...textProps} />}
          </Field>
        </div>
      )}

      <fieldset className={styles.group} aria-describedby={sourceReadOnly ? id(`ro-${ro('build') ? 'build' : 'image'}`) : undefined}>
        <legend className={styles.title}>Source</legend>
        {readonlyNote(ro('build') ? 'build' : 'image')}
        <div className={styles.segmented} role="radiogroup" aria-label="Source type">
          {(['image', 'build'] as const).map((value) => (
            <label key={value} className={styles.seg}>
              <input type="radio" name={radioName} value={value} checked={form.source === value} disabled={sourceReadOnly} onChange={() => patch({ source: value })} />
              <span className="visually-hidden">From</span> {value === 'image' ? 'Image' : 'Dockerfile'}
            </label>
          ))}
        </div>
        {form.source === 'image' ? (
          <Field label="Image" id={id('image')} mono error={shown['image']} hint="For example nginx:1.27" readonly={sourceReadOnly}>
            {(aria) => <input type="text" {...aria} value={form.image} onChange={(e) => patch({ image: e.target.value })} {...textProps} />}
          </Field>
        ) : (
          <>
            <Field label="Build context" id={id('build-context')} mono error={shown['build.context']} readonly={sourceReadOnly}>
              {(aria) => (
                <div className={styles.inline}>
                  <input type="text" {...aria} value={form.buildContext} onChange={(e) => patch({ buildContext: e.target.value })} {...textProps} />
                  {onBrowse && !sourceReadOnly && (
                    <Button variant="secondary" aria-label="Browse for build context" onClick={() => onBrowse('dir', (p) => patch({ buildContext: p }))}>
                      Browse…
                    </Button>
                  )}
                </div>
              )}
            </Field>
            <Field label="Dockerfile" id={id('dockerfile')} mono error={shown['build.dockerfile']} hint="Optional; defaults to Dockerfile in the context" readonly={sourceReadOnly}>
              {(aria) => (
                <div className={styles.inline}>
                  <input type="text" {...aria} value={form.dockerfile} onChange={(e) => patch({ dockerfile: e.target.value })} {...textProps} />
                  {onBrowse && !sourceReadOnly && (
                    <Button variant="secondary" aria-label="Browse for Dockerfile" onClick={() => onBrowse('file', (p) => patch({ dockerfile: p }))}>
                      Browse…
                    </Button>
                  )}
                </div>
              )}
            </Field>
            <Field label="Image tag" id={id('image')} mono error={shown['image']} hint="Optional; tag applied to the built image" readonly={sourceReadOnly}>
              {(aria) => <input type="text" {...aria} value={form.image} onChange={(e) => patch({ image: e.target.value })} {...textProps} />}
            </Field>
          </>
        )}
      </fieldset>

      <fieldset className={styles.group} aria-label="Ports" aria-describedby={describedBy('ports')}>
        {readonlyNote('ports')}
        <ListEditor
          title="Ports"
          hint="host:container"
          items={form.ports}
          onChange={(ports) => patch({ ports })}
          addLabel="Add port"
          newItem={() => ''}
          readonly={ro('ports')}
          errors={portErrors}
          row={(item, i, update) => (
            <input
              type="text"
              id={id(`ports-${i}`)}
              aria-label={`Port ${i + 1}`}
              aria-invalid={invalid(`ports[${i}]`)}
              readOnly={ro('ports')}
              value={item}
              placeholder="8080:80"
              onChange={(e) => update(e.target.value)}
              {...textProps}
            />
          )}
        />
      </fieldset>

      <fieldset className={styles.group} aria-label="Volumes" aria-describedby={describedBy('volumes')}>
        {readonlyNote('volumes')}
        <ListEditor
          title="Volumes"
          hint="host:container[:ro]"
          items={form.volumes}
          onChange={(volumes) => patch({ volumes })}
          addLabel="Add volume"
          newItem={() => ''}
          readonly={ro('volumes')}
          errors={volumeErrors}
          row={(item, i, update) => (
            <input
              type="text"
              id={id(`volumes-${i}`)}
              aria-label={`Volume ${i + 1}`}
              aria-invalid={invalid(`volumes[${i}]`)}
              readOnly={ro('volumes')}
              value={item}
              placeholder="./data:/var/lib/data"
              onChange={(e) => update(e.target.value)}
              {...textProps}
            />
          )}
        />
      </fieldset>

      <fieldset className={styles.group} aria-label="Environment" aria-describedby={describedBy('environment')}>
        {readonlyNote('environment')}
        <ListEditor
          title="Environment"
          hint="paste a .env to add many"
          items={form.env}
          onChange={(env) => patch({ env })}
          addLabel="Add variable"
          newItem={() => ({ name: '', value: '' })}
          readonly={ro('environment')}
          errors={envErrors}
          row={(item, i, update) => (
            <>
              <input
                type="text"
                id={id(`env-${i}-name`)}
                aria-label={`Variable ${i + 1} name`}
                aria-invalid={invalid(`env[${i}].name`)}
                readOnly={ro('environment')}
                value={item.name}
                placeholder="NAME"
                onChange={(e) => update({ ...item, name: e.target.value })}
                onPaste={(e) => pasteEnv(e, i)}
                {...textProps}
              />
              <input
                type="text"
                id={id(`env-${i}-value`)}
                aria-label={`Variable ${i + 1} value`}
                aria-invalid={invalid(`env[${i}].value`)}
                readOnly={ro('environment')}
                value={item.value}
                placeholder="value"
                onChange={(e) => update({ ...item, value: e.target.value })}
                {...textProps}
              />
            </>
          )}
        />
      </fieldset>

      <fieldset className={styles.group} aria-label="Networks" aria-describedby={describedBy('networks')}>
        {readonlyNote('networks')}
        {networkPicker && !ro('networks') ? (
          networkPicker(form, networkErrors)
        ) : (
          <ListEditor
            title="Networks"
            items={form.networks}
            onChange={(networks) => patch({ networks })}
            addLabel="Add network"
            newItem={() => ({ name: '', ipv4Address: '' })}
            readonly={ro('networks')}
            errors={networkErrors}
            row={(item, i, update) => (
              <>
                <input
                  type="text"
                  id={id(`networks-${i}-name`)}
                  aria-label={`Network ${i + 1} name`}
                  aria-invalid={invalid(`networks[${i}].name`)}
                  readOnly={ro('networks')}
                  value={item.name}
                  placeholder="network"
                  onChange={(e) => update({ ...item, name: e.target.value })}
                  {...textProps}
                />
                <input
                  type="text"
                  id={id(`networks-${i}-ip`)}
                  aria-label={`Network ${i + 1} IPv4 address`}
                  aria-invalid={invalid(`networks[${i}].ipv4Address`)}
                  readOnly={ro('networks')}
                  value={item.ipv4Address}
                  placeholder="IPv4 (optional)"
                  onChange={(e) => update({ ...item, ipv4Address: e.target.value })}
                  {...textProps}
                />
              </>
            )}
          />
        )}
      </fieldset>

      <fieldset className={styles.group} aria-label="Network mode group" aria-describedby={describedBy('network_mode')}>
        {readonlyNote('network_mode')}
        <Field label="Network mode" id={id('network-mode')} mono error={shown['networkMode']} readonly={ro('network_mode')}>
          {({ readOnly, ...aria }) => (
            <>
              <select {...aria} disabled={readOnly} value={customMode ? 'custom' : form.networkMode} onChange={(e) => selectMode(e.target.value)}>
                <option value="">Default</option>
                <option value="host">host</option>
                <option value="none">none</option>
                <option value="bridge">bridge</option>
                <option value="custom">Custom…</option>
              </select>
              {customMode && (
                <input
                  type="text"
                  id={id('network-mode-custom')}
                  aria-label="Custom network mode"
                  aria-describedby={aria['aria-describedby']}
                  aria-invalid={aria['aria-invalid']}
                  readOnly={readOnly}
                  value={form.networkMode}
                  onChange={(e) => patch({ networkMode: e.target.value })}
                  placeholder="service:name or container:name"
                  {...textProps}
                />
              )}
            </>
          )}
        </Field>
      </fieldset>

      <fieldset className={styles.group} aria-describedby={describedBy('healthcheck')}>
        <legend className={styles.title}>Healthcheck</legend>
        {readonlyNote('healthcheck')}
        <label className={styles.check}>
          <input type="checkbox" id={id('hc-enabled')} checked={form.healthcheck.enabled} disabled={ro('healthcheck')} onChange={(e) => patchHealthcheck({ enabled: e.target.checked })} />
          Enable healthcheck
        </label>
        {form.healthcheck.enabled && (
          <>
            <Field label="Healthcheck command" id={id('hc-test')} mono error={shown['healthcheck.test']} hint="Shell command, or start with CMD for exec form" readonly={ro('healthcheck')}>
              {(aria) => <input type="text" {...aria} value={form.healthcheck.test} onChange={(e) => patchHealthcheck({ test: e.target.value })} {...textProps} />}
            </Field>
            <Field label="Interval" id={id('hc-interval')} mono error={shown['healthcheck.interval']} hint="For example 30s" readonly={ro('healthcheck')}>
              {(aria) => <input type="text" {...aria} value={form.healthcheck.interval} onChange={(e) => patchHealthcheck({ interval: e.target.value })} {...textProps} />}
            </Field>
            <Field label="Timeout" id={id('hc-timeout')} mono error={shown['healthcheck.timeout']} readonly={ro('healthcheck')}>
              {(aria) => <input type="text" {...aria} value={form.healthcheck.timeout} onChange={(e) => patchHealthcheck({ timeout: e.target.value })} {...textProps} />}
            </Field>
            <Field label="Start period" id={id('hc-start-period')} mono error={shown['healthcheck.startPeriod']} readonly={ro('healthcheck')}>
              {(aria) => <input type="text" {...aria} value={form.healthcheck.startPeriod} onChange={(e) => patchHealthcheck({ startPeriod: e.target.value })} {...textProps} />}
            </Field>
            <Field label="Retries" id={id('hc-retries')} mono error={shown['healthcheck.retries']} readonly={ro('healthcheck')}>
              {(aria) => <input type="text" {...aria} value={form.healthcheck.retries} onChange={(e) => patchHealthcheck({ retries: e.target.value })} inputMode="numeric" autoComplete="off" />}
            </Field>
          </>
        )}
      </fieldset>

      <div className={styles.grid3}>
        <fieldset className={styles.group} aria-label="Restart policy group" aria-describedby={describedBy('restart')}>
          {readonlyNote('restart')}
          <Field label="Restart policy" id={id('restart')} error={shown['restart']} readonly={ro('restart')}>
            {({ readOnly, ...aria }) => (
              <select {...aria} disabled={readOnly} value={form.restart} onChange={(e) => patch({ restart: e.target.value })}>
                {RESTART.map(([value, label]) => (
                  <option key={value} value={value}>
                    {label}
                  </option>
                ))}
                {!RESTART.some(([value]) => value === form.restart) && <option value={form.restart}>{form.restart}</option>}
              </select>
            )}
          </Field>
        </fieldset>
        <fieldset className={`${styles.group} ${styles.limits}`} aria-label="Resources" aria-describedby={describedBy('limits')}>
          {readonlyNote('limits')}
          <Field label="CPUs" id={id('cpus')} mono error={shown['limits.cpus']} hint="For example 0.5" readonly={ro('limits')}>
            {(aria) => <input type="text" {...aria} value={form.cpus} onChange={(e) => patch({ cpus: e.target.value })} inputMode="decimal" autoComplete="off" />}
          </Field>
          <Field label="Memory" id={id('memory')} mono error={shown['limits.memory']} hint="For example 512M or 1G" readonly={ro('limits')}>
            {(aria) => <input type="text" {...aria} value={form.memory} onChange={(e) => patch({ memory: e.target.value })} {...textProps} />}
          </Field>
        </fieldset>
      </div>
    </div>
  );
}
