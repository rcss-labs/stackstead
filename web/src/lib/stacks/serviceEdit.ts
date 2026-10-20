import { get, writable, type Readable, type Writable } from '$lib/store';
import { ApiError } from '../api/client';
import type { DockerfileContent, DockerfileView, Field, RawCompose, ServiceConfig, StackDetail } from '../api/types';
import { fromForm, mapServerErrors, toForm, validateForm, type FormErrors, type ServiceForm } from '../compose/form';

export type EditPhase = 'editing' | 'busy' | 'previewing' | 'saving' | 'conflict' | 'saved';
/** A message shown above the form: either "use the raw editor" or a plain failure. */
export type EditNotice = { kind: 'rawEditor' | 'error'; message: string };

export interface ServiceEditDeps {
  preview: (config: ServiceConfig, hash: string) => Promise<{ content: string; hash: string }>;
  /** `dockerfile` is sent only when the Dockerfile itself changed; its hash is the one loaded ('' = must not exist yet). */
  update: (config: ServiceConfig, hash: string, dockerfile?: DockerfileContent) => Promise<{ hash: string; checksPartial?: boolean }>;
  /** Fetch this service's Dockerfile. A failure leaves the editor working on the build entry alone. */
  loadDockerfile?: () => Promise<DockerfileView>;
  compose: () => Promise<RawCompose>;
  /** Fetch the stack again (used when the compose file moved past the hash the detail carries). */
  reloadDetail: () => Promise<StackDetail>;
}

// "this change needs the raw YAML editor" and the read-only-field "can only be edited as raw YAML".
const RAW_EDITOR_RE = /raw yaml/i;

// Form fields per editable group, used to tell which groups the user changed.
const GROUPS: (keyof ServiceForm)[][] = [
  ['source', 'image', 'buildContext', 'dockerfile', 'dockerfileAt'],
  ['ports'],
  ['volumes'],
  ['env'],
  ['networks'],
  ['networkMode'],
  ['cpus', 'memory'],
  ['restart'],
  ['healthcheck'],
  ['dockerfileText'],
];
const GROUP_LABELS = ['Image', 'Ports', 'Volumes', 'Environment', 'Networks', 'Network mode', 'Limits', 'Restart policy', 'Healthcheck', 'Dockerfile'];
const GROUP_NAMES: Field[][] = [['image', 'build'], ['ports'], ['volumes'], ['environment'], ['networks'], ['network_mode'], ['limits'], ['restart'], ['healthcheck'], ['build']];

/**
 * The form for a service, given the Dockerfile view when there is one: an editable existing file becomes the
 * 'dockerfile' source; one the form may not edit leaves the build as read-only text.
 */
function seedForm(cfg: ServiceConfig, readOnly: Field[], view: DockerfileView | undefined): ServiceForm {
  const locked: Field[] = view && !view.editable && !readOnly.includes('build') ? [...readOnly, 'build'] : readOnly;
  const f = toForm(cfg, locked);
  if (view?.editable && view.exists && cfg.build) {
    f.source = 'dockerfile';
    f.dockerfileText = view.content;
    f.dockerfileAt = { context: cfg.build.context, dockerfile: cfg.build.dockerfile ?? '' };
  }
  return f;
}

const cloneForm = (f: ServiceForm): ServiceForm => ({ ...f, readOnly: new Set(f.readOnly) });

/**
 * New file's form with only the groups the user changed (vs. the form they started from) laid on top.
 * `discarded` names changed groups that cannot be edited now: the file's values win for those.
 */
function rebaseForm(user: ServiceForm, seeded: ServiceForm, fresh: ServiceForm): { form: ServiceForm; discarded: string[] } {
  const out = cloneForm(fresh);
  const discarded: string[] = [];
  GROUPS.forEach((keys, i) => {
    const changed = keys.some((k) => JSON.stringify(user[k]) !== JSON.stringify(seeded[k]));
    if (GROUP_NAMES[i].some((g) => fresh.readOnly.has(g))) {
      if (changed) discarded.push(GROUP_LABELS[i]);
      return;
    }
    if (changed) for (const k of keys) (out as unknown as Record<string, unknown>)[k] = user[k];
  });
  return { form: out, discarded };
}

// Stable text of a form for dirty checks (Sets do not survive JSON.stringify).
const fingerprint = (f: ServiceForm) => JSON.stringify({ ...f, readOnly: [...f.readOnly].sort() });
const messageOf = (e: unknown) => (e instanceof Error ? e.message : String(e));

/**
 * State machine for editing one service through the form: validate → preview (against the
 * current file) → save with the very hash the preview used. A 409 never overwrites; the caller
 * reloads the stack and calls reload()/discard().
 */
export function createServiceEdit(
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
  /** The service's Dockerfile as last loaded; undefined when there is none or it could not be loaded. */
  dockerfileView: Readable<DockerfileView | undefined>;
  /** The last refusal was because the Dockerfile changed on disk. */
  dockerfileConflict: Readable<boolean>;
  /** The file changed under the editor and the user's changes were applied on top of the new version. */
  rebased: Readable<boolean>;
  /** Groups whose edits a rebase dropped because the new file only allows editing them as raw YAML. */
  discardedGroups: Readable<string[]>;
  /** Bumped whenever the machine replaces the form itself (rebase, discard); the view re-reads `form`. */
  formVersion: Readable<number>;
  preview(): Promise<void>;
  save(): Promise<boolean>;
  cancelPreview(): void;
  reload(detail: StackDetail): Promise<void>;
  discard(detail: StackDetail): Promise<void>;
} {
  let original = initial.config;
  let hashValue = initial.hash;
  let seeded = toForm(initial.config, initial.readOnly);
  let baseline = fingerprint(seeded);
  // The Dockerfile's hash, and the build entry it was loaded for: a different build means a different file.
  let loadedHash = '';
  let loadedBuild: string | undefined;
  // What the preview was computed from; the save sends exactly this.
  let previewed: { config: ServiceConfig; hash: string; form: ServiceForm; dockerfile?: DockerfileContent } | undefined;

  const form = writable<ServiceForm>(toForm(initial.config, initial.readOnly));
  const errors = writable<FormErrors>({});
  const phase = writable<EditPhase>('editing');
  const notice = writable<EditNotice | undefined>(undefined);
  const conflictMessage = writable<string | undefined>(undefined);
  const hash = writable(hashValue);
  const checksPartial = writable(false);
  const dirty = writable(false);
  const rebased = writable(false);
  const discardedGroups = writable<string[]>([]);
  const formVersion = writable(0);
  const previewText = writable<{ original: string; modified: string } | undefined>(undefined);
  const dockerfileView = writable<DockerfileView | undefined>(undefined);
  const dockerfileConflict = writable(false);
  let rebasedOnce = false;

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

  /** Load the Dockerfile when the service builds one; undefined (no promise) when there is nothing to load. */
  function loadView(cfg: ServiceConfig, readOnly: Field[]): Promise<DockerfileView | undefined> | undefined {
    if (!deps.loadDockerfile || !(cfg.build || readOnly.includes('build'))) return undefined;
    return deps.loadDockerfile().catch(() => undefined);
  }

  /**
   * The view after a re-fetch. A failed fetch keeps the editable view the user is working from (a moved file
   * then 409s on the next save) instead of silently dropping their Dockerfile edit.
   */
  async function resolveView(pending: Promise<DockerfileView | undefined> | undefined): Promise<DockerfileView | undefined> {
    if (!pending) return undefined;
    const fetched = await pending;
    if (fetched) return fetched;
    const previous = get(dockerfileView);
    return previous?.editable && previous.exists ? previous : undefined;
  }

  function rememberView(view: DockerfileView | undefined, cfg: ServiceConfig) {
    dockerfileView.set(view);
    const usable = view?.editable === true && view.exists;
    loadedHash = usable ? view.hash : '';
    loadedBuild = usable ? JSON.stringify(cfg.build ?? null) : undefined;
  }

  /** The Dockerfile to send with a save: only when it changed or became the source, and with the hash it was loaded at. */
  function dockerfileToSend(config: ServiceConfig, f: ServiceForm): DockerfileContent | undefined {
    if (f.source !== 'dockerfile') return undefined;
    if (f.dockerfileText === seeded.dockerfileText && seeded.source === 'dockerfile') return undefined;
    const sameFile = loadedBuild !== undefined && loadedBuild === JSON.stringify(config.build ?? null);
    return { content: f.dockerfileText, hash: sameFile ? loadedHash : '' };
  }

  /** Take the stack's new state: new original, hash and read-only set; the user's changed groups go on top. */
  function rebase(detail: StackDetail, view: DockerfileView | undefined): boolean {
    const svc = detail.services.find((x) => x.name === service);
    if (!svc) {
      notice.set({ kind: 'error', message: `Service "${service}" no longer exists in this stack.` });
      return false;
    }
    rebasedOnce = true;
    rememberView(view, svc.config);
    const fresh = seedForm(svc.config, svc.readOnly, view);
    const { form: merged, discarded } = rebaseForm(get(form), seeded, fresh);
    original = svc.config;
    setHash(detail.hash);
    seeded = fresh;
    baseline = fingerprint(fresh);
    form.set(merged);
    formVersion.update((n) => n + 1);
    rebased.set(true);
    discardedGroups.set(discarded);
    return true;
  }

  function fail(e: unknown) {
    previewText.set(undefined);
    previewed = undefined;
    if (e instanceof ApiError) {
      if (e.status === 409) {
        dockerfileConflict.set(/dockerfile changed on disk/i.test(e.message));
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

  // The Dockerfile arrives after the first render; changes made meanwhile are laid on top of it.
  const firstView = loadView(initial.config, initial.readOnly);
  if (firstView) {
    void firstView.then((view) => {
      if (!view || rebasedOnce || get(phase) !== 'editing') return;
      rememberView(view, original);
      const fresh = seedForm(original, initial.readOnly, view);
      const { form: merged } = rebaseForm(get(form), seeded, fresh);
      seeded = fresh;
      baseline = fingerprint(fresh);
      form.set(merged);
      formVersion.update((n) => n + 1);
    });
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
    dockerfileView: { subscribe: dockerfileView.subscribe },
    dockerfileConflict: { subscribe: dockerfileConflict.subscribe },
    rebased: { subscribe: rebased.subscribe },
    discardedGroups: { subscribe: discardedGroups.subscribe },
    formVersion: { subscribe: formVersion.subscribe },

    async preview() {
      const phaseNow = get(phase);
      if (phaseNow !== 'editing' && phaseNow !== 'saved') return;
      notice.set(undefined);
      const found = validateForm(get(form), { original });
      errors.set(found);
      if (Object.keys(found).length > 0) return;
      phase.set('busy');
      try {
        let current = await deps.compose();
        if (current.hash !== hashValue) {
          // The file moved on since this page loaded; lay the user's changes on top of its new state.
          const fresh = await deps.reloadDetail();
          const target = fresh.services.find((x) => x.name === service);
          const pending = target && loadView(target.config, target.readOnly);
          if (!rebase(fresh, pending ? await resolveView(pending) : undefined)) {
            phase.set('editing');
            return;
          }
          current = await deps.compose();
          if (current.hash !== hashValue) {
            conflictMessage.set('The compose file changed while you were editing. Reload to continue.');
            phase.set('conflict');
            return;
          }
          const again = validateForm(get(form), { original });
          errors.set(again);
          if (Object.keys(again).length > 0) {
            phase.set('editing');
            return;
          }
        }
        const snapshot = cloneForm(get(form));
        const config = fromForm(snapshot, original, service);
        const sentHash = hashValue;
        const dockerfile = dockerfileToSend(config, snapshot);
        const res = await deps.preview(config, sentHash);
        previewed = { config, hash: sentHash, form: snapshot, dockerfile };
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
        const res = await deps.update(sent.config, sent.hash, sent.dockerfile);
        original = sent.config;
        // The write returns only the compose hash: read the Dockerfile again so the next save has its new hash.
        if (sent.dockerfile) {
          const after = loadView(sent.config, []);
          const view = after ? await after : undefined;
          if (view) rememberView(view, sent.config);
        }
        setHash(res.hash);
        // The baseline is what was actually saved; edits typed during the request stay dirty.
        seeded = sent.form;
        baseline = fingerprint(sent.form);
        dirty.set(fingerprint(get(form)) !== baseline);
        rebased.set(false);
        discardedGroups.set([]);
        checksPartial.set(res.checksPartial === true);
        previewText.set(undefined);
        previewed = undefined;
        phase.set(get(dirty) ? 'editing' : 'saved');
        return true;
      } catch (e) {
        fail(e);
        return false;
      }
    },

    async reload(detail) {
      const target = detail.services.find((x) => x.name === service);
      const pending = target && loadView(target.config, target.readOnly);
      if (!rebase(detail, pending ? await resolveView(pending) : undefined)) return;
      conflictMessage.set(undefined);
      dockerfileConflict.set(false);
      notice.set(undefined);
      previewText.set(undefined);
      previewed = undefined;
      phase.set('editing');
    },

    async discard(detail) {
      const svc = detail.services.find((x) => x.name === service);
      if (!svc) {
        notice.set({ kind: 'error', message: `Service "${service}" no longer exists in this stack.` });
        return;
      }
      const pending = loadView(svc.config, svc.readOnly);
      const view = pending ? await resolveView(pending) : undefined;
      rebasedOnce = true;
      rememberView(view, svc.config);
      original = svc.config;
      setHash(detail.hash);
      seeded = seedForm(svc.config, svc.readOnly, view);
      baseline = fingerprint(seeded);
      form.set(cloneForm(seeded));
      formVersion.update((n) => n + 1);
      rebased.set(false);
      discardedGroups.set([]);
      errors.set({});
      conflictMessage.set(undefined);
      dockerfileConflict.set(false);
      notice.set(undefined);
      previewText.set(undefined);
      previewed = undefined;
      phase.set('editing');
    },
  };
}
