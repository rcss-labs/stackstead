<script lang="ts">
  import { tick } from 'svelte';
  import { api, ApiError } from '../lib/api/client';
  import Banner from '../lib/components/Banner.svelte';
  import Button from '../lib/components/Button.svelte';
  import Field from '../lib/components/Field.svelte';
  import { emptyForm, fromForm, groupServiceErrors, mapServerErrors, validateForm, type FormErrors, type ServiceForm as ServiceFormState } from '../lib/compose/form';
  import { link, navigate } from '../lib/router';
  import FileBrowser from '../lib/stacks/FileBrowser.svelte';
  import NetworkPicker from '../lib/stacks/NetworkPicker.svelte';
  import ServiceForm from '../lib/stacks/ServiceForm.svelte';
  import { refreshRunningOps, refreshStacks } from '../lib/stores/stacks';
  import { toasts } from '../lib/stores/toasts';

  let stackName = $state('');
  type Card = { id: number; name: string; autoName: boolean; form: ServiceFormState; errors: FormErrors };
  let nextId = 1;
  let cards = $state<Card[]>([{ id: 0, name: '', autoName: true, form: emptyForm(), errors: {} }]);
  let openId = $state(0);
  let formError = $state('');
  let startNow = $state(true);
  let stackNameError = $state('');
  let unavailable = $state('');
  let pending = $state(false);
  let serviceFormRef = $state<ServiceForm>();

  let browser = $state<{ mode: 'dir' | 'file'; cb: (path: string) => void } | null>(null);

  // Only the initial card follows the stack name, until its name is edited.
  $effect(() => {
    if (cards[0]?.autoName) cards[0].name = stackName;
  });

  $effect(() => {
    for (const card of cards) {
      const stale = card.form.source === 'build' ? (k: string) => k === 'image' : (k: string) => k.startsWith('build.');
      for (const key of Object.keys(card.errors)) if (stale(key)) delete card.errors[key];
    }
  });

  async function addService() {
    if (pending || browser || cards.length >= 20) return;
    const card: Card = { id: nextId++, name: '', autoName: false, form: emptyForm(), errors: {} };
    cards.push(card);
    openId = card.id;
    await tick();
    document.getElementById('svc-name')?.focus();
  }

  function removeService(id: number) {
    if (pending || browser || cards.length === 1) return;
    const index = cards.findIndex((card) => card.id === id);
    if (openId === id) openId = cards[index > 0 ? index - 1 : 1].id;
    cards.splice(index, 1);
  }

  function errorCount(card: Card) {
    return Object.keys(card.errors).filter((key) => key !== '_form').length;
  }

  $effect(() => {
    document.title = 'Create stack · Stackstead';
  });

  function onbrowse(card: Card, mode: 'dir' | 'file', cb: (path: string) => void) {
    if (pending) return;
    const context = card.form.buildContext;
    browser =
      mode === 'file'
        ? {
            mode,
            // A Dockerfile inside the build context is stored relative to it, otherwise absolute.
            cb: (path) => {
              const raw = context.trim();
              const base = raw.replace(/\/+$/, ''); // '' for the root context "/"
              cb(raw && path.startsWith(`${base}/`) ? path.slice(base.length + 1) : path);
            },
          }
        : { mode, cb };
  }

  async function focusFirstError() {
    const first = cards.find((card) => errorCount(card) > 0);
    if (first) openId = first.id;
    await tick();
    if (unavailable) document.getElementById('create-unavailable')?.focus();
    else if (stackNameError) document.getElementById('stack-name')?.focus();
    else if (first) serviceFormRef?.focusFirstError();
    else if (formError) document.getElementById('create-error')?.focus();
  }

  async function submit(event: SubmitEvent) {
    event.preventDefault();
    if (pending || browser) return;
    unavailable = '';
    formError = '';
    stackNameError = validateForm(cards[0].form, { name: stackName, requireName: true, original: null }).name ?? '';
    const names = new Set<string>();
    for (const card of cards) {
      card.errors = validateForm(card.form, { name: card.name, requireName: true, original: null });
      if (names.has(card.name)) card.errors.name = `duplicate service name ${card.name}`;
      names.add(card.name);
    }
    if (stackNameError || cards.some((card) => errorCount(card) > 0)) {
      await focusFirstError();
      return;
    }
    const submitted = cards.map((card) => card.id);
    const body = { name: stackName, services: cards.map((card) => ({ service: card.name, config: fromForm(card.form, null) })) };
    pending = true;
    try {
      const { data, checksPartial } = await api.createStack(body);
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
        formError = 'Could not reach the server. Check your connection and try again.';
      } else if (err.status === 400) {
        const { cards: grouped, rest } = groupServiceErrors(err.fields);
        const messages: string[] = [];
        for (const [index, fields] of grouped) {
          const card = cards.find((candidate) => candidate.id === submitted[index]);
          if (!card) {
            messages.push(...fields.map((field) => field.message));
            continue;
          }
          const mapped = mapServerErrors(fields, card.form);
          if (mapped._form) messages.push(mapped._form);
          delete mapped._form;
          card.errors = mapped;
        }
        for (const field of rest) {
          if (field.field === 'name') stackNameError = stackNameError ? `${stackNameError}; ${field.message}` : field.message;
          else messages.push(field.message);
        }
        formError = messages.join('\n');
        if (err.fields.length === 0) formError = err.message;
      } else if (err.status === 409) {
        stackNameError = err.message;
      } else if (err.status === 503) {
        unavailable = err.message;
      } else {
        formError = err.message;
      }
      pending = false;
      await focusFirstError();
    } finally {
      pending = false;
    }
  }
</script>

<div class="page">
  <h1>New stack</h1>
  <p class="lead">One or more services. Stackstead writes <code>compose.yaml</code> into a folder named after the stack in its stacks directory.</p>
  {#if unavailable}
    <div id="create-unavailable" tabindex="-1">
      <Banner kind="danger" title="Docker/compose unavailable: the stack was not created">{unavailable}</Banner>
    </div>
  {/if}
  <form class="card" onsubmit={submit} novalidate>
    <div class="stack-name">
      <Field label="Stack name" id="stack-name" mono
        hint="Lowercase letters, digits, '-' and '_'; starts with a letter or digit; at most 63 characters." error={stackNameError}>
        {#snippet children(c)}
          <input {...c} type="text" bind:value={stackName} disabled={pending} autocomplete="off" spellcheck="false" autocapitalize="off" />
        {/snippet}
      </Field>
    </div>
    {#if formError}
      <div id="create-error" class="form-error" role="alert" tabindex="-1">{formError}</div>
    {/if}
    {#each cards as card, index (card.id)}
      <section aria-labelledby="service-heading-{card.id}">
        <div class="service-header">
          <h2>
            <button id="service-heading-{card.id}" type="button" class="service-toggle"
              aria-expanded={openId === card.id} aria-controls="service-body-{card.id}"
              disabled={pending || browser !== null} onclick={() => (openId = card.id)}>
              <span class="service-title">{card.name || `Service ${index + 1}`}</span>
              <span class="summary">{card.form.source === 'build' ? `build: ${card.form.buildContext}` : card.form.image}</span>
              {#if openId !== card.id && errorCount(card) > 0}
                <span class="error-marker"><span aria-hidden="true">⚠</span> {errorCount(card)} {errorCount(card) === 1 ? 'error' : 'errors'}</span>
              {/if}
            </button>
          </h2>
          {#if cards.length > 1}
            <Button variant="ghost" icon aria-label="Remove service {card.name || `Service ${index + 1}`}"
              disabled={pending || browser !== null} onclick={() => removeService(card.id)}>✕</Button>
          {/if}
        </div>
        <div id="service-body-{card.id}" hidden={openId !== card.id}>
          {#if openId === card.id}
            <fieldset disabled={pending}>
              <ServiceForm bind:this={serviceFormRef} bind:form={card.form}
                bind:name={() => card.name, (value) => { card.name = value; card.autoName = false; }}
                errors={card.errors} showName onbrowse={(mode, cb) => onbrowse(card, mode, cb)}>
                {#snippet networkPicker(f, rowErrors)}
                  <NetworkPicker value={f.networks} onchange={(v) => (f.networks = v)} errors={rowErrors} idPrefix="svc-networks" />
                {/snippet}
              </ServiceForm>
            </fieldset>
          {/if}
        </div>
      </section>
    {/each}
    <div class="add-service">
      <Button variant="secondary" disabled={pending || browser !== null}
        disabledReason={cards.length >= 20 ? 'A stack can have at most 20 services.' : undefined} onclick={addService}>+ Add service</Button>
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
  .stack-name, .add-service { padding: 14px; }
  section { border-top: 1px solid var(--border); }
  .service-header { display: flex; align-items: center; gap: 10px; padding: 8px 14px; }
  h2 { flex: 1; min-width: 0; font-size: 13px; margin: 0; }
  .service-toggle { display: flex; flex-wrap: wrap; align-items: center; gap: 8px 14px; width: 100%; border: 0; background: transparent; color: var(--text); text-align: left; font: inherit; padding: 6px 0; cursor: pointer; }
  .service-toggle:disabled { cursor: not-allowed; opacity: .6; }
  .service-title { font-weight: 600; overflow-wrap: anywhere; }
  .summary { color: var(--muted); font-family: var(--mono); font-size: 12px; overflow-wrap: anywhere; }
  .error-marker { color: var(--danger); font-size: 12px; }
  fieldset { border: 0; padding: 0; margin: 0; min-width: 0; }
  .form-error { color: var(--danger); background: var(--danger-soft); padding: 12px 14px; margin: 0 14px 14px; white-space: pre-wrap; overflow-wrap: anywhere; font-size: 13px; }
  .footer { display: flex; flex-wrap: wrap; align-items: center; gap: 14px; padding: 12px 14px; border-top: 1px solid var(--border); }
  .check { display: flex; gap: 8px; align-items: center; font-size: 12.5px; }
  .spacer { flex: 1; }
  .footer a { color: var(--muted); font-size: 13px; }
</style>
