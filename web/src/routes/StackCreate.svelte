<script lang="ts">
  import { tick } from 'svelte';
  import { api, ApiError } from '../lib/api/client';
  import Banner from '../lib/components/Banner.svelte';
  import Button from '../lib/components/Button.svelte';
  import Field from '../lib/components/Field.svelte';
  import { emptyForm, fromForm, mapServerErrors, validateForm, type FormErrors } from '../lib/compose/form';
  import { link, navigate } from '../lib/router';
  import FileBrowser from '../lib/stacks/FileBrowser.svelte';
  import NetworkPicker from '../lib/stacks/NetworkPicker.svelte';
  import ServiceForm from '../lib/stacks/ServiceForm.svelte';
  import { refreshRunningOps, refreshStacks } from '../lib/stores/stacks';
  import { toasts } from '../lib/stores/toasts';

  let stackName = $state('');
  let serviceName = $state('');
  let autoServiceName = '';
  let form = $state(emptyForm());
  let startNow = $state(true);
  let errors = $state<FormErrors>({});
  let stackNameError = $state('');
  let unavailable = $state('');
  let pending = $state(false);
  let serviceFormRef = $state<ServiceForm>();

  let browser = $state<{ mode: 'dir' | 'file'; cb: (path: string) => void } | null>(null);

  // The service name follows the stack name until the user edits it.
  $effect(() => {
    if (serviceName === autoServiceName) {
      autoServiceName = stackName;
      serviceName = stackName;
    }
  });

  // Errors for the field group that is no longer on screen would only confuse.
  $effect(() => {
    const stale = form.source === 'build' ? (k: string) => k === 'image' : (k: string) => k.startsWith('build.');
    for (const key of Object.keys(errors)) if (stale(key)) delete errors[key];
  });

  $effect(() => {
    document.title = 'Create stack · Stackstead';
  });

  function onbrowse(mode: 'dir' | 'file', cb: (path: string) => void) {
    browser =
      mode === 'file'
        ? {
            mode,
            // A Dockerfile inside the build context is stored relative to it, otherwise absolute.
            cb: (path) => {
              const raw = form.buildContext.trim();
              const base = raw.replace(/\/+$/, ''); // '' for the root context "/"
              cb(raw && path.startsWith(`${base}/`) ? path.slice(base.length + 1) : path);
            },
          }
        : { mode, cb };
  }

  async function focusFirstError() {
    await tick();
    if (unavailable) document.getElementById('create-unavailable')?.focus();
    else if (stackNameError) document.getElementById('stack-name')?.focus();
    else serviceFormRef?.focusFirstError();
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (pending) return;
    unavailable = '';
    stackNameError = validateForm(form, { name: stackName, requireName: true, original: null }).name ?? '';
    const svcErrors = validateForm(form, { name: serviceName, requireName: true, original: null });
    errors = svcErrors;
    if (stackNameError || Object.keys(svcErrors).length > 0) {
      await focusFirstError();
      return;
    }
    pending = true;
    try {
      const { data, checksPartial } = await api.createStack({ name: stackName, service: serviceName, config: fromForm(form, null) });
      void refreshStacks();
      refreshRunningOps();
      toasts.push({
        kind: checksPartial ? 'warning' : 'success',
        message: checksPartial
          ? `Stack ${data.name} created, but port/IP conflict checks could not run (Docker unreachable).`
          : `Stack ${data.name} created`,
      });
      let query = 'tab=operations';
      if (startNow) {
        try {
          const op = await api.act(data.name, 'up');
          refreshRunningOps();
          query += `&op=${encodeURIComponent(op.id)}`;
        } catch (err) {
          toasts.push({ kind: 'danger', message: `Stack created, but starting it failed: ${err instanceof Error ? err.message : String(err)}` });
        }
      }
      navigate(`/stacks/${encodeURIComponent(data.name)}?${query}`);
    } catch (err) {
      if (!(err instanceof ApiError)) {
        errors = { _form: 'Could not reach the server. Check your connection and try again.' };
      } else if (err.status === 400) {
        const mapped = mapServerErrors(err.fields, form);
        stackNameError = mapped.name ?? '';
        delete mapped.name;
        errors = Object.keys(mapped).length === 0 && !stackNameError ? { _form: err.message } : mapped;
      } else if (err.status === 409) {
        stackNameError = err.message;
        errors = {};
      } else if (err.status === 403) {
        errors = { _form: err.message };
      } else if (err.status === 503) {
        unavailable = err.message;
        errors = {};
      } else {
        errors = { _form: err.message };
      }
      await focusFirstError();
    } finally {
      pending = false;
    }
  }
</script>

<div class="page">
  <h1>New stack</h1>
  <p class="lead">One service to start with. Stackstead writes <code>compose.yaml</code> into a folder named after the stack in its stacks directory.</p>
  {#if unavailable}
    <div id="create-unavailable" tabindex="-1">
      <Banner kind="danger" title="Docker/compose unavailable: the stack was not created">{unavailable}</Banner>
    </div>
  {/if}
  <form class="card" onsubmit={submit} novalidate>
    <div class="svc">
      <ServiceForm bind:this={serviceFormRef} bind:form bind:name={serviceName} {errors} showName {onbrowse}>
        {#snippet lead()}
          <Field
            label="Stack name"
            id="stack-name"
            mono
            hint="Lowercase letters, digits, '-' and '_'; starts with a letter or digit; at most 63 characters."
            error={stackNameError}
          >
            {#snippet children(c)}
              <input {...c} type="text" bind:value={stackName} autocomplete="off" spellcheck="false" autocapitalize="off" />
            {/snippet}
          </Field>
        {/snippet}
        {#snippet networkPicker(f, rowErrors)}
          <NetworkPicker value={f.networks} onchange={(v) => (f.networks = v)} errors={rowErrors} idPrefix="svc-networks" />
        {/snippet}
      </ServiceForm>
    </div>

    <div class="footer">
      <label class="check">
        <input type="checkbox" bind:checked={startNow} />
        Start now (<code>up --build</code>)
      </label>
      <span class="spacer"></span>
      <a href="/" use:link>Cancel</a>
      <Button variant="primary" type="submit" loading={pending}>Create stack</Button>
    </div>
  </form>
</div>

<FileBrowser
  open={browser !== null}
  mode={browser?.mode ?? 'dir'}
  title={browser?.mode === 'file' ? 'Choose a Dockerfile' : 'Choose the build context'}
  onpick={(path) => {
    browser?.cb(path);
    browser = null;
  }}
  onclose={() => (browser = null)}
/>

<style>
  .page { max-width: 720px; display: flex; flex-direction: column; gap: 12px; }
  .lead { color: var(--muted); font-size: 13px; }
  code { font-family: var(--mono); font-size: 12px; }
  .card { background: var(--surface); border: 1px solid var(--border); border-radius: var(--radius-md); }
  .svc :global(.form-error) { white-space: pre-wrap; }
  .footer { display: flex; flex-wrap: wrap; align-items: center; gap: 14px; padding: 12px 14px; border-top: 1px solid var(--border); }
  .check { display: flex; gap: 8px; align-items: center; font-size: 12.5px; }
  .spacer { flex: 1; }
  .footer a { color: var(--muted); font-size: 13px; }
</style>
