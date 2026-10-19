import { derived, get, writable } from '$lib/store';
import { ApiError } from '../api/client';
import type { RawCompose, ServiceConfig, StackDetail } from '../api/types';
import { emptyForm, fromForm, mapServerErrors, validateForm, type FormErrors } from '../compose/form';
export type AddPhase = 'editing' | 'busy' | 'previewing' | 'saving' | 'conflict';
export interface ServiceAddDeps {
  preview: (service: string, config: ServiceConfig, hash: string) => Promise<{
    content: string;
    hash: string;
  }>;
  add: (service: string, config: ServiceConfig, hash: string) => Promise<{
    detail: StackDetail;
    checksPartial: boolean;
  }>;
  compose: () => Promise<RawCompose>;
  reloadDetail: () => Promise<StackDetail>;
}
const fingerprint = (f: ReturnType<typeof emptyForm>) => JSON.stringify({ ...f, readOnly: [...f.readOnly] });
export function createServiceAdd(initialHash: string, deps: ServiceAddDeps) {
  const form = writable(emptyForm());
  const name = writable('');
  const errors = writable<FormErrors>({});
  const phase = writable<AddPhase>('editing');
  const notice = writable<{
    message: string;
  } | undefined>();
  const conflictMessage = writable<string | undefined>();
  const hash = writable(initialHash);
  const checksPartial = writable(false);
  const previewText = writable<{
    original: string;
    modified: string;
  } | undefined>();
  const baseline = fingerprint(emptyForm());
  const dirty = derived([form, name], ([$form, $name]) => !!$name || fingerprint($form) !== baseline);
  let generation = 0;
  let disposed = false;
  let previewed: {
    service: string;
    config: ServiceConfig;
    hash: string;
  } | undefined;
  const current = (mine: number) => !disposed && mine === generation;
  function conflict(message: string) {
    conflictMessage.set(message);
    phase.set('conflict');
  }
  function clear() {
    previewed = undefined;
    previewText.set(undefined);
    notice.set(undefined);
    conflictMessage.set(undefined);
    errors.set({});
  }
  function fail(e: unknown) {
    previewed = undefined;
    previewText.set(undefined);
    if (e instanceof ApiError && e.status === 409) {
      conflict(e.message);
      return;
    }
    if (e instanceof ApiError && e.status === 400)
      errors.set(e.fields.length ? mapServerErrors(e.fields, get(form)) : { _form: e.message });
    else
      notice.set({ message: e instanceof Error ? e.message : String(e) });
    phase.set('editing');
  }
  function reload(detail: StackDetail) {
    if (disposed)
      return;
    generation++;
    clear();
    hash.set(detail.hash);
    phase.set('editing');
  }
  return {
    form, name, errors: { subscribe: errors.subscribe }, phase: { subscribe: phase.subscribe }, notice: { subscribe: notice.subscribe },
    conflictMessage: { subscribe: conflictMessage.subscribe }, hash: { subscribe: hash.subscribe }, checksPartial: { subscribe: checksPartial.subscribe },
    previewText: { subscribe: previewText.subscribe }, dirty,
    async preview() {
      if (disposed || get(phase) !== 'editing')
        return;
      clear();
      const found = validateForm(get(form), { name: get(name), requireName: true, original: null });
      errors.set(found);
      if (Object.keys(found).length)
        return;
      // Capture before the first await: even programmatic form changes cannot alter this candidate.
      const candidate = { service: get(name), config: fromForm(get(form), null) };
      const mine = generation;
      phase.set('busy');
      try {
        let raw = await deps.compose();
        if (!current(mine))
          return;
        if (raw.hash !== get(hash)) {
          const fresh = await deps.reloadDetail();
          if (!current(mine))
            return;
          hash.set(fresh.hash);
          raw = await deps.compose();
          if (!current(mine))
            return;
          if (raw.hash !== fresh.hash) {
            conflict('The compose file changed while you were editing. Reload to continue.');
            return;
          }
        }
        const sent = { ...candidate, hash: raw.hash };
        const result = await deps.preview(sent.service, sent.config, sent.hash);
        if (!current(mine))
          return;
        if (result.hash !== sent.hash) {
          conflict('The compose file changed while you were editing. Reload to continue.');
          return;
        }
        previewed = sent;
        previewText.set({ original: raw.content, modified: result.content });
        phase.set('previewing');
      }
      catch (e) {
        if (current(mine))
          fail(e);
      }
    },
    async save(): Promise<StackDetail | undefined> {
      if (disposed || get(phase) !== 'previewing' || !previewed)
        return;
      const sent = previewed;
      const mine = generation;
      phase.set('saving');
      try {
        const res = await deps.add(sent.service, sent.config, sent.hash);
        if (!current(mine))
          return;
        checksPartial.set(res.checksPartial);
        hash.set(res.detail.hash);
        clear();
        phase.set('editing');
        return res.detail;
      }
      catch (e) {
        if (current(mine))
          fail(e);
      }
    },
    cancelPreview() {
      if (!disposed && get(phase) === 'previewing') {
        clear();
        phase.set('editing');
      }
    },
    reload,
    discard(detail: StackDetail) {
      reload(detail);
      if (!disposed) {
        form.set(emptyForm());
        name.set('');
      }
    },
    dispose() {
      disposed = true;
      generation++;
    },
  };
}
