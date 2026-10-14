import { get, writable, type Readable, type Writable } from 'svelte/store';
import { ApiError } from '../api/client';
import type { Field, RawCompose, ServiceConfig, StackDetail } from '../api/types';
import { fromForm, mapServerErrors, toForm, validateForm, type FormErrors, type ServiceForm } from '../compose/form';

export type EditPhase = 'editing' | 'previewing' | 'saving' | 'conflict' | 'saved';
/** A message shown above the form: either "use the raw editor" or a plain failure. */
export type EditNotice = { kind: 'rawEditor' | 'error'; message: string };

export interface ServiceEditDeps {
  preview: (config: ServiceConfig, hash: string) => Promise<{ content: string; hash: string }>;
  update: (config: ServiceConfig, hash: string) => Promise<{ hash: string; checksPartial?: boolean }>;
  compose: () => Promise<RawCompose>;
  /** Fetch the stack again (used when the compose file moved past the hash the detail carries). */
  reloadDetail: () => Promise<StackDetail>;
}

const RAW_EDITOR_RE = /raw yaml editor/i;

// Stable text of a form for dirty checks (Sets do not survive JSON.stringify).
const fingerprint = (f: ServiceForm) => JSON.stringify({ ...f, readOnly: [...f.readOnly].sort() });
const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

/**
 * State machine for editing one service through the form: validate → preview (against the
 * current file) → save with the very hash the preview used. A 409 never overwrites; the caller
 * reloads the stack and calls reload()/discard().
 */
export function createServiceEdit(
  stack: string,
  service: string,
  initial: { config: ServiceConfig; readOnly: Field[]; hash: string },
  deps: ServiceEditDeps,
): {
  form: Writable<ServiceForm>;
  errors: Readable<FormErrors>;
  phase: Readable<EditPhase>;
  notice: Readable<EditNotice | undefined>;
  conflictMessage: Readable<string | undefined>;
  hash: Readable<string>;
  checksPartial: Readable<boolean>;
  dirty: Readable<boolean>;
  previewText: Readable<{ original: string; modified: string } | undefined>;
  preview(): Promise<void>;
  save(): Promise<boolean>;
  cancelPreview(): void;
  reload(detail: StackDetail): void;
  discard(detail: StackDetail): void;
} {
  void stack;
  let original = initial.config;
  let hashValue = initial.hash;
  let baseline = fingerprint(toForm(initial.config, initial.readOnly));
  // What the preview was computed from; the save sends exactly this.
  let previewed: { config: ServiceConfig; hash: string } | undefined;

  const form = writable<ServiceForm>(toForm(initial.config, initial.readOnly));
  const errors = writable<FormErrors>({});
  const phase = writable<EditPhase>('editing');
  const notice = writable<EditNotice | undefined>(undefined);
  const conflictMessage = writable<string | undefined>(undefined);
  const hash = writable(hashValue);
  const checksPartial = writable(false);
  const dirty = writable(false);
  const previewText = writable<{ original: string; modified: string } | undefined>(undefined);

  form.subscribe((f) => {
    const isDirty = fingerprint(f) !== baseline;
    dirty.set(isDirty);
    // Editing again after a save starts a new edit.
    if (isDirty && get(phase) === 'saved') phase.set('editing');
  });

  const setHash = (h: string) => {
    hashValue = h;
    hash.set(h);
  };

  function adopt(detail: StackDetail): ServiceConfig | undefined {
    const svc = detail.services.find((s) => s.name === service);
    if (!svc) {
      notice.set({ kind: 'error', message: `Service "${service}" no longer exists in this stack.` });
      return undefined;
    }
    original = svc.config;
    setHash(detail.hash);
    return svc.config;
  }

  function adoptReadOnly(detail: StackDetail) {
    const svc = detail.services.find((s) => s.name === service);
    if (svc) form.update((f) => ({ ...f, readOnly: new Set<Field>(svc.readOnly) }));
  }

  function fail(e: unknown) {
    previewText.set(undefined);
    previewed = undefined;
    if (e instanceof ApiError) {
      if (e.status === 409) {
        conflictMessage.set(e.message);
        phase.set('conflict');
        return;
      }
      if (e.status === 400 || e.status === 422) {
        if (e.status === 422 && RAW_EDITOR_RE.test(e.message)) {
          notice.set({ kind: 'rawEditor', message: e.message });
        } else if (e.fields.length > 0) {
          errors.set(mapServerErrors(e.fields, get(form), service));
        } else if (e.status === 400) {
          errors.set({ _form: e.message });
        } else {
          notice.set({ kind: 'error', message: e.message });
        }
        phase.set('editing');
        return;
      }
    }
    notice.set({ kind: 'error', message: messageOf(e) });
    phase.set('editing');
  }

  return {
    form,
    errors: { subscribe: errors.subscribe },
    phase: { subscribe: phase.subscribe },
    notice: { subscribe: notice.subscribe },
    conflictMessage: { subscribe: conflictMessage.subscribe },
    hash: { subscribe: hash.subscribe },
    checksPartial: { subscribe: checksPartial.subscribe },
    dirty: { subscribe: dirty.subscribe },
    previewText: { subscribe: previewText.subscribe },

    async preview() {
      const phaseNow = get(phase);
      if (phaseNow === 'saving' || phaseNow === 'conflict') return;
      notice.set(undefined);
      const f = get(form);
      const found = validateForm(f, { original });
      errors.set(found);
      if (Object.keys(found).length > 0) return;
      try {
        let current = await deps.compose();
        if (current.hash !== hashValue) {
          // The file moved on since this page loaded; pick up its new state before previewing.
          const fresh = await deps.reloadDetail();
          if (!adopt(fresh)) {
            phase.set('editing');
            return;
          }
          adoptReadOnly(fresh);
          current = await deps.compose();
          if (current.hash !== hashValue) {
            conflictMessage.set('The compose file changed while you were editing. Reload to continue.');
            phase.set('conflict');
            return;
          }
        }
        const config = fromForm(get(form), original);
        const sentHash = hashValue;
        const res = await deps.preview(config, sentHash);
        previewed = { config, hash: sentHash };
        previewText.set({ original: current.content, modified: res.content });
        phase.set('previewing');
      } catch (e) {
        fail(e);
      }
    },

    cancelPreview() {
      if (get(phase) === 'previewing') {
        previewText.set(undefined);
        previewed = undefined;
        phase.set('editing');
      }
    },

    async save() {
      if (get(phase) !== 'previewing' || !previewed) return false;
      const sent = previewed;
      notice.set(undefined);
      phase.set('saving');
      try {
        const res = await deps.update(sent.config, sent.hash);
        original = sent.config;
        setHash(res.hash);
        baseline = fingerprint(get(form));
        dirty.set(false);
        checksPartial.set(res.checksPartial === true);
        previewText.set(undefined);
        previewed = undefined;
        phase.set('saved');
        return true;
      } catch (e) {
        fail(e);
        return false;
      }
    },

    reload(detail) {
      const cfg = adopt(detail);
      if (!cfg) return;
      adoptReadOnly(detail);
      conflictMessage.set(undefined);
      notice.set(undefined);
      previewText.set(undefined);
      previewed = undefined;
      phase.set('editing');
    },

    discard(detail) {
      const svc = detail.services.find((s) => s.name === service);
      if (!adopt(detail) || !svc) return;
      const fresh = toForm(svc.config, svc.readOnly);
      baseline = fingerprint(fresh);
      form.set(fresh);
      errors.set({});
      conflictMessage.set(undefined);
      notice.set(undefined);
      previewText.set(undefined);
      previewed = undefined;
      phase.set('editing');
    },
  };
}
