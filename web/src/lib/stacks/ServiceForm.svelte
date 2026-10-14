<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { Field as FieldGroup } from '../api/types';
  import type { FormErrors, ServiceForm } from '../compose/form';
  import Button from '../components/Button.svelte';
  import Field from '../components/Field.svelte';
  import ListEditor from '../components/ListEditor.svelte';

  let {
    form = $bindable(),
    errors,
    showName = false,
    name = $bindable(''),
    nameReadonly = false,
    onbrowse,
    networkPicker,
    onRawEditor,
  }: {
    form: ServiceForm;
    errors: FormErrors;
    showName?: boolean;
    name?: string;
    nameReadonly?: boolean;
    onbrowse?: (mode: 'dir' | 'file', cb: (path: string) => void) => void;
    networkPicker?: Snippet<[ServiceForm, Record<number, string>]>;
    onRawEditor?: () => void;
  } = $props();

  const RO_HINT = "This field uses YAML the form can't edit — open the raw YAML editor";
  const RESTART = [
    ['', 'Default (no restart)'],
    ['no', 'no'],
    ['always', 'always'],
    ['unless-stopped', 'unless-stopped'],
    ['on-failure', 'on-failure'],
  ] as const;
  const MODE_PRESETS = ['', 'host', 'none', 'bridge'];

  let root = $state<HTMLElement>();
  let pickedCustom = $state(false);
  const customMode = $derived(pickedCustom || !MODE_PRESETS.includes(form.networkMode));

  const ro = (g: FieldGroup) => form.readOnly.has(g);
  const sourceReadOnly = $derived(ro('image') || ro('build'));

  // Errors for the source that is not selected are not shown: they no longer apply to anything on screen.
  const shown = $derived.by(() => {
    const out: FormErrors = {};
    for (const [path, message] of Object.entries(errors)) {
      if (form.source === 'image' && path.startsWith('build.') && !sourceReadOnly) continue;
      if (path.startsWith('healthcheck.') && !form.healthcheck.enabled && !ro('healthcheck')) continue;
      out[path] = message;
    }
    return out;
  });
  const errorCount = $derived(Object.keys(shown).length);

  function rowErrors(prefix: string, suffixes: string[] = ['']): Record<number, string> {
    const out: Record<number, string> = {};
    for (const [path, message] of Object.entries(shown)) {
      for (const s of suffixes) {
        const m = new RegExp(`^${prefix}\\[(\\d+)\\]${s.replace('.', '\\.')}$`).exec(path);
        if (m) out[Number(m[1])] = out[Number(m[1])] ? `${out[Number(m[1])]}; ${message}` : message;
      }
    }
    return out;
  }
  const portErrors = $derived(rowErrors('ports'));
  const volumeErrors = $derived(rowErrors('volumes'));
  const envErrors = $derived(rowErrors('env', ['.name', '.value']));
  const networkErrors = $derived(rowErrors('networks', ['.name', '.ipv4Address']));

  const invalid = (path: string) => (shown[path] ? ('true' as const) : undefined);

  export function focusFirstError() {
    const target =
      root?.querySelector<HTMLElement>('[aria-invalid="true"]') ?? root?.querySelector<HTMLElement>('[data-form-error]');
    target?.focus();
  }

  function browse(mode: 'dir' | 'file', set: (path: string) => void) {
    onbrowse?.(mode, set);
  }

  function selectMode(value: string) {
    if (value === 'custom') {
      pickedCustom = true;
      form.networkMode = '';
    } else {
      pickedCustom = false;
      form.networkMode = value;
    }
  }

  function pasteEnv(event: ClipboardEvent, index: number) {
    if (ro('environment')) return;
    const row = form.env[index];
    if (!row || row.name !== '' || row.value !== '') return;
    const text = event.clipboardData?.getData('text') ?? '';
    const pairs: { name: string; value: string }[] = [];
    for (const raw of text.split(/\r?\n/)) {
      const line = raw.trim();
      if (line === '' || line.startsWith('#')) continue;
      const eq = line.indexOf('=');
      if (eq <= 0) return; // not an env dump: let the browser paste it as plain text
      pairs.push({ name: line.slice(0, eq).trim(), value: line.slice(eq + 1) });
    }
    if (pairs.length === 0) return;
    event.preventDefault();
    form.env = [...form.env.slice(0, index), ...pairs, ...form.env.slice(index + 1)];
  }
</script>

{#snippet readonlyNote(group: FieldGroup)}
  {#if ro(group)}
    <p class="note" id="svc-ro-{group}">
      {RO_HINT}
      {#if onRawEditor}<Button variant="secondary" aria-label="Open raw YAML editor for {group}" onclick={() => onRawEditor?.()}>Open raw YAML editor</Button>{/if}
    </p>
  {/if}
{/snippet}

<div class="service-form" bind:this={root}>
  <p class="visually-hidden" role="status">{errorCount > 0 ? `${errorCount} ${errorCount === 1 ? 'field has an error' : 'fields have errors'}: ${Object.keys(shown).join(', ')}` : ''}</p>
  {#if errors['_form']}
    <div class="form-error" role="alert" tabindex="-1" data-form-error>{errors['_form']}</div>
  {/if}

  {#if showName}
    <Field label="Service name" id="svc-name" error={shown['name']} readonly={nameReadonly} readonlyReason="The name of an existing service cannot be changed here.">
      {#snippet children(aria)}
        <input type="text" {...aria} bind:value={name} autocomplete="off" spellcheck="false" />
      {/snippet}
    </Field>
  {/if}

  <fieldset aria-describedby={sourceReadOnly ? `svc-ro-${ro('build') ? 'build' : 'image'}` : undefined}>
    <legend>Source</legend>
    {@render readonlyNote(ro('build') ? 'build' : 'image')}
    <div class="radios" role="radiogroup" aria-label="Source type">
      <label><input type="radio" name="svc-source" value="image" checked={form.source === 'image'} disabled={sourceReadOnly} onchange={() => (form.source = 'image')} /> From image</label>
      <label><input type="radio" name="svc-source" value="build" checked={form.source === 'build'} disabled={sourceReadOnly} onchange={() => (form.source = 'build')} /> From build</label>
    </div>
    {#if form.source === 'image'}
      <Field label="Image" id="svc-image" error={shown['image']} hint="For example nginx:1.27" readonly={sourceReadOnly}>
        {#snippet children(aria)}
          <input type="text" {...aria} bind:value={form.image} autocomplete="off" spellcheck="false" />
        {/snippet}
      </Field>
    {:else}
      <Field label="Image tag" id="svc-image" error={shown['image']} hint="Optional; tag applied to the built image" readonly={sourceReadOnly}>
        {#snippet children(aria)}
          <input type="text" {...aria} bind:value={form.image} autocomplete="off" spellcheck="false" />
        {/snippet}
      </Field>
      <Field label="Build context" id="svc-build-context" error={shown['build.context']} readonly={sourceReadOnly}>
        {#snippet children(aria)}
          <div class="inline">
            <input type="text" {...aria} bind:value={form.buildContext} autocomplete="off" spellcheck="false" />
            {#if onbrowse && !sourceReadOnly}
              <Button variant="secondary" aria-label="Browse for build context" onclick={() => browse('dir', (p) => (form.buildContext = p))}>Browse…</Button>
            {/if}
          </div>
        {/snippet}
      </Field>
      <Field label="Dockerfile" id="svc-dockerfile" error={shown['build.dockerfile']} hint="Optional; defaults to Dockerfile in the context" readonly={sourceReadOnly}>
        {#snippet children(aria)}
          <div class="inline">
            <input type="text" {...aria} bind:value={form.dockerfile} autocomplete="off" spellcheck="false" />
            {#if onbrowse && !sourceReadOnly}
              <Button variant="secondary" aria-label="Browse for Dockerfile" onclick={() => browse('file', (p) => (form.dockerfile = p))}>Browse…</Button>
            {/if}
          </div>
        {/snippet}
      </Field>
    {/if}
  </fieldset>

  <fieldset aria-describedby={ro('ports') ? 'svc-ro-ports' : undefined}>
    <legend>Ports</legend>
    {@render readonlyNote('ports')}
    <ListEditor items={form.ports} onchange={(v) => (form.ports = v)} addLabel="Add port" newItem={() => ''} readonly={ro('ports')} errors={portErrors}>
      {#snippet row(item, i, update)}
        <input
          type="text"
          id="svc-ports-{i}"
          aria-label="Port {i + 1}"
          aria-invalid={invalid(`ports[${i}]`)}
          readonly={ro('ports')}
          value={item}
          placeholder="8080:80"
          autocomplete="off"
          spellcheck="false"
          oninput={(e) => update(e.currentTarget.value)}
        />
      {/snippet}
    </ListEditor>
  </fieldset>

  <fieldset aria-describedby={ro('volumes') ? 'svc-ro-volumes' : undefined}>
    <legend>Volumes</legend>
    {@render readonlyNote('volumes')}
    <ListEditor items={form.volumes} onchange={(v) => (form.volumes = v)} addLabel="Add volume" newItem={() => ''} readonly={ro('volumes')} errors={volumeErrors}>
      {#snippet row(item, i, update)}
        <input
          type="text"
          id="svc-volumes-{i}"
          aria-label="Volume {i + 1}"
          aria-invalid={invalid(`volumes[${i}]`)}
          readonly={ro('volumes')}
          value={item}
          placeholder="./data:/var/lib/data"
          autocomplete="off"
          spellcheck="false"
          oninput={(e) => update(e.currentTarget.value)}
        />
      {/snippet}
    </ListEditor>
  </fieldset>

  <fieldset aria-describedby={ro('environment') ? 'svc-ro-environment' : undefined}>
    <legend>Environment</legend>
    {@render readonlyNote('environment')}
    <ListEditor
      items={form.env}
      onchange={(v) => (form.env = v)}
      addLabel="Add variable"
      newItem={() => ({ name: '', value: '' })}
      readonly={ro('environment')}
      errors={envErrors}
    >
      {#snippet row(item, i, update)}
        <input
          type="text"
          id="svc-env-{i}-name"
          aria-label="Variable {i + 1} name"
          aria-invalid={invalid(`env[${i}].name`)}
          readonly={ro('environment')}
          value={item.name}
          placeholder="NAME"
          autocomplete="off"
          spellcheck="false"
          oninput={(e) => update({ ...item, name: e.currentTarget.value })}
          onpaste={(e) => pasteEnv(e, i)}
        />
        <input
          type="text"
          id="svc-env-{i}-value"
          aria-label="Variable {i + 1} value"
          aria-invalid={invalid(`env[${i}].value`)}
          readonly={ro('environment')}
          value={item.value}
          placeholder="value"
          autocomplete="off"
          spellcheck="false"
          oninput={(e) => update({ ...item, value: e.currentTarget.value })}
        />
      {/snippet}
    </ListEditor>
  </fieldset>

  <fieldset aria-describedby={ro('networks') ? 'svc-ro-networks' : undefined}>
    <legend>Networks</legend>
    {@render readonlyNote('networks')}
    {#if networkPicker && !ro('networks')}
      {@render networkPicker(form, networkErrors)}
    {:else}
      <ListEditor
        items={form.networks}
        onchange={(v) => (form.networks = v)}
        addLabel="Add network"
        newItem={() => ({ name: '', ipv4Address: '' })}
        readonly={ro('networks')}
        errors={networkErrors}
      >
        {#snippet row(item, i, update)}
          <input
            type="text"
            id="svc-networks-{i}-name"
            aria-label="Network {i + 1} name"
            aria-invalid={invalid(`networks[${i}].name`)}
            readonly={ro('networks')}
            value={item.name}
            placeholder="network"
            autocomplete="off"
            spellcheck="false"
            oninput={(e) => update({ ...item, name: e.currentTarget.value })}
          />
          <input
            type="text"
            id="svc-networks-{i}-ip"
            aria-label="Network {i + 1} IPv4 address"
            aria-invalid={invalid(`networks[${i}].ipv4Address`)}
            readonly={ro('networks')}
            value={item.ipv4Address}
            placeholder="IPv4 (optional)"
            autocomplete="off"
            spellcheck="false"
            oninput={(e) => update({ ...item, ipv4Address: e.currentTarget.value })}
          />
        {/snippet}
      </ListEditor>
    {/if}
  </fieldset>

  <fieldset aria-describedby={ro('network_mode') ? 'svc-ro-network_mode' : undefined}>
    <legend>Network mode</legend>
    {@render readonlyNote('network_mode')}
    <Field label="Network mode" id="svc-network-mode" error={shown['networkMode']} readonly={ro('network_mode')}>
      {#snippet children(aria)}
        {@const { readonly: isReadonly, ...attrs } = aria}
        <select {...attrs} disabled={isReadonly} value={customMode ? 'custom' : form.networkMode} onchange={(e) => selectMode(e.currentTarget.value)}>
          <option value="">Default</option>
          <option value="host">host</option>
          <option value="none">none</option>
          <option value="bridge">bridge</option>
          <option value="custom">Custom…</option>
        </select>
        {#if customMode}
          <input
            type="text"
            id="svc-network-mode-custom"
            aria-label="Custom network mode"
            aria-describedby={aria['aria-describedby']}
            aria-invalid={aria['aria-invalid']}
            readonly={isReadonly}
            bind:value={form.networkMode}
            placeholder="service:name or container:name"
            autocomplete="off"
            spellcheck="false"
          />
        {/if}
      {/snippet}
    </Field>
  </fieldset>

  <fieldset aria-describedby={ro('limits') ? 'svc-ro-limits' : undefined}>
    <legend>Resources</legend>
    {@render readonlyNote('limits')}
    <Field label="CPUs" id="svc-cpus" error={shown['limits.cpus']} hint="For example 0.5" readonly={ro('limits')}>
      {#snippet children(aria)}
        <input type="text" {...aria} bind:value={form.cpus} inputmode="decimal" autocomplete="off" />
      {/snippet}
    </Field>
    <Field label="Memory" id="svc-memory" error={shown['limits.memory']} hint="For example 512M or 1G" readonly={ro('limits')}>
      {#snippet children(aria)}
        <input type="text" {...aria} bind:value={form.memory} autocomplete="off" spellcheck="false" />
      {/snippet}
    </Field>
  </fieldset>

  <fieldset aria-describedby={ro('restart') ? 'svc-ro-restart' : undefined}>
    <legend>Restart</legend>
    {@render readonlyNote('restart')}
    <Field label="Restart policy" id="svc-restart" error={shown['restart']} readonly={ro('restart')}>
      {#snippet children(aria)}
        {@const { readonly: isReadonly, ...attrs } = aria}
        <select {...attrs} disabled={isReadonly} bind:value={form.restart}>
          {#each RESTART as [value, label] (value)}
            <option {value}>{label}</option>
          {/each}
          {#if !RESTART.some(([v]) => v === form.restart)}
            <option value={form.restart}>{form.restart}</option>
          {/if}
        </select>
      {/snippet}
    </Field>
  </fieldset>

  <fieldset aria-describedby={ro('healthcheck') ? 'svc-ro-healthcheck' : undefined}>
    <legend>Healthcheck</legend>
    {@render readonlyNote('healthcheck')}
    <label class="check">
      <input type="checkbox" id="svc-hc-enabled" bind:checked={form.healthcheck.enabled} disabled={ro('healthcheck')} />
      Enable healthcheck
    </label>
    {#if form.healthcheck.enabled}
      <Field label="Healthcheck command" id="svc-hc-test" error={shown['healthcheck.test']} hint="Shell command, or start with CMD for exec form" readonly={ro('healthcheck')}>
        {#snippet children(aria)}
          <input type="text" {...aria} bind:value={form.healthcheck.test} autocomplete="off" spellcheck="false" />
        {/snippet}
      </Field>
      <Field label="Interval" id="svc-hc-interval" error={shown['healthcheck.interval']} hint="For example 30s" readonly={ro('healthcheck')}>
        {#snippet children(aria)}
          <input type="text" {...aria} bind:value={form.healthcheck.interval} autocomplete="off" spellcheck="false" />
        {/snippet}
      </Field>
      <Field label="Timeout" id="svc-hc-timeout" error={shown['healthcheck.timeout']} readonly={ro('healthcheck')}>
        {#snippet children(aria)}
          <input type="text" {...aria} bind:value={form.healthcheck.timeout} autocomplete="off" spellcheck="false" />
        {/snippet}
      </Field>
      <Field label="Start period" id="svc-hc-start-period" error={shown['healthcheck.startPeriod']} readonly={ro('healthcheck')}>
        {#snippet children(aria)}
          <input type="text" {...aria} bind:value={form.healthcheck.startPeriod} autocomplete="off" spellcheck="false" />
        {/snippet}
      </Field>
      <Field label="Retries" id="svc-hc-retries" error={shown['healthcheck.retries']} readonly={ro('healthcheck')}>
        {#snippet children(aria)}
          <input type="text" {...aria} bind:value={form.healthcheck.retries} inputmode="numeric" autocomplete="off" />
        {/snippet}
      </Field>
    {/if}
  </fieldset>
</div>

<style>
  .service-form { display: flex; flex-direction: column; gap: var(--space-4); }
  fieldset { display: flex; flex-direction: column; gap: var(--space-3); border: 1px solid var(--border); border-radius: var(--radius-md); padding: var(--space-3) var(--space-4); }
  legend { font-weight: 700; padding: 0 var(--space-2); }
  .form-error { color: var(--danger); border: 1px solid var(--danger); border-radius: var(--radius-md); padding: var(--space-3); overflow-wrap: anywhere; }
  .note { color: var(--muted); font-size: 0.875rem; display: flex; gap: var(--space-3); align-items: center; flex-wrap: wrap; }
  .radios, .check { display: flex; gap: var(--space-4); align-items: center; }
  .inline { display: flex; gap: var(--space-2); }
  .inline input { flex: 1; min-width: 0; }
</style>
