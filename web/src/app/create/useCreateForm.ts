import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { FormEvent, RefObject } from 'react';
import { useNavigate } from 'react-router';
import { ApiError } from '$lib/api/client';
import type { CreateStackRequest, CreateStackService } from '$lib/api/types';
import { defaultImageTag, emptyForm, fromForm, groupServiceErrors, mapServerErrors, validateForm } from '$lib/compose/form';
import type { FormErrors, ServiceForm } from '$lib/compose/form';
import { stackToYaml, yamlToStack } from '$lib/compose/stackYaml';
import type { Extra } from '$lib/compose/stackYaml';
import { isCreateDirty } from '$lib/stacks/createPreview';
import { setUnsavedChanges } from '$lib/stores/guard';
import { toasts } from '$lib/stores/toasts';
import { fieldLabel } from '../stacks/ServiceForm';
import type { BrowsePurpose, ServiceFormHandle } from '../stacks/ServiceForm';

import { useServer } from '../servers/ServerContext';
export const MAX_SERVICES = 20;

export interface Card {
  id: number;
  name: string;
  autoName: boolean;
  form: ServiceForm;
  /** Compose keys of this service that the form does not model; they travel with the request. */
  extra: Extra;
  errors: FormErrors;
}

export type EditorMode = 'form' | 'yaml';

export interface YamlError {
  message: string;
  line?: number;
}

/** What an empty New stack screen turns into when switched to YAML: there is nothing to convert yet. */
const EMPTY_DOCUMENT = 'services: {}\n';
export const YAML_BLOCKED_REASON = 'Fix the YAML error first';
export const NO_SERVICE_REASON = 'Add a service first';
const YAML_CHECK_DELAY_MS = 150;

export interface BrowserState {
  purpose: BrowsePurpose;
  mode: 'dir' | 'file';
  cb: (path: string) => void;
}

export interface CreateForm {
  stackName: string;
  setStackName: (name: string) => void;
  cards: Card[];
  openId: number;
  open: (id: number) => void;
  addCard: () => void;
  removeCard: (id: number) => void;
  setCardName: (id: number, name: string) => void;
  setCardForm: (id: number, form: ServiceForm) => void;
  mode: EditorMode;
  setMode: (mode: EditorMode) => void;
  yamlText: string;
  setYamlText: (text: string) => void;
  yamlError: YamlError | null;
  yamlFieldErrors: string[];
  stackExtra: Extra;
  /** Why Create cannot be pressed right now, or '' when it can. */
  createBlocked: string;
  /** Lets a card adjust the form before a new source is applied (the default image tag of a written Dockerfile). */
  sourceChange: (cardId: number, source: ServiceForm['source']) => ServiceForm | void;
  startNow: boolean;
  setStartNow: (value: boolean) => void;
  pending: boolean;
  formError: string;
  stackNameError: string;
  unavailable: string;
  browser: BrowserState | null;
  browse: (cardId: number, mode: 'dir' | 'file', set: (path: string) => void, purpose: BrowsePurpose) => void;
  pick: (path: string) => void;
  closeBrowser: () => void;
  errorCount: (card: Card) => number;
  submit: (event: FormEvent) => Promise<void>;
  focusFirstError: () => void;
  serviceFormRef: RefObject<ServiceFormHandle | null>;
}

type PendingFocus = { kind: 'errors' } | { kind: 'element'; id: string };

const errorCount = (card: Card) => Object.keys(card.errors).filter((key) => key !== '_form').length;

/**
 * State machine of the New stack screen. Values that asynchronous code reads after an await (the cards, the
 * pending flag, the open browser) live in refs that are updated together with their state, so a late server
 * response sees the cards that were submitted, not the render that started the request.
 */
export function useCreateForm(): CreateForm {
  const navigate = useNavigate();
  const { api, trackOperation, refreshRunningOps, refreshStacks, href } = useServer();
  const nextId = useRef(1);
  const serviceFormRef = useRef<ServiceFormHandle | null>(null);

  const [stackName, setStackNameState] = useState('');
  const stackNameRef = useRef('');
  const [cards, setCardsState] = useState<Card[]>(() => [{ id: 0, name: '', autoName: true, form: emptyForm(), extra: {}, errors: {} }]);
  const cardsRef = useRef(cards);
  const [openId, setOpenId] = useState(0);
  const [mode, setModeState] = useState<EditorMode>('form');
  const modeRef = useRef<EditorMode>('form');
  const [yamlText, setYamlTextState] = useState('');
  const yamlTextRef = useRef('');
  const [yamlError, setYamlError] = useState<YamlError | null>(null);
  // The text parses but declares no service: Create stays disabled without an error line.
  const [yamlEmpty, setYamlEmpty] = useState(false);
  const [yamlFieldErrors, setYamlFieldErrors] = useState<string[]>([]);
  const [yamlEdited, setYamlEdited] = useState(false);
  const [stackExtra, setStackExtraState] = useState<Extra>({});
  const stackExtraRef = useRef<Extra>({});
  // The text of the last switch, restored as is (comments kept) while the cards have not been edited since.
  const storedYamlRef = useRef<string | null>(null);
  const formEditedRef = useRef(false);
  // Decision 14: written Dockerfile texts by service name, so a service that leaves and re-enters the document keeps its text.
  const dockerfilesRef = useRef<Record<string, string>>({});
  const checkTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [startNow, setStartNow] = useState(true);
  const startNowRef = useRef(true);
  const [pending, setPendingState] = useState(false);
  const pendingRef = useRef(false);
  const [formError, setFormError] = useState('');
  const [stackNameError, setStackNameError] = useState('');
  const [unavailable, setUnavailable] = useState('');
  const [browser, setBrowserState] = useState<BrowserState | null>(null);
  const browserRef = useRef<BrowserState | null>(null);

  // The React equivalent of `await tick()`: a request is queued, a render bumps the counter, and the layout
  // effect below runs once the new state is in the DOM.
  const pendingFocus = useRef<PendingFocus | null>(null);
  const [focusRequest, setFocusRequest] = useState(0);

  function setCards(update: (current: Card[]) => Card[]) {
    cardsRef.current = update(cardsRef.current);
    setCardsState(cardsRef.current);
  }
  const patchCard = (id: number, changes: (card: Card) => Partial<Card>) =>
    setCards((current) => current.map((card) => (card.id === id ? { ...card, ...changes(card) } : card)));
  function setPending(value: boolean) {
    pendingRef.current = value;
    setPendingState(value);
  }
  function setBrowser(value: BrowserState | null) {
    browserRef.current = value;
    setBrowserState(value);
  }
  function requestFocus(target: PendingFocus) {
    pendingFocus.current = target;
    setFocusRequest((n) => n + 1);
  }

  useLayoutEffect(() => {
    const target = pendingFocus.current;
    if (!target) return;
    pendingFocus.current = null;
    if (target.kind === 'element') {
      document.getElementById(target.id)?.focus();
      return;
    }
    const first = cards.find((card) => errorCount(card) > 0);
    if (unavailable) document.getElementById('create-unavailable')?.focus();
    else if (stackNameError) document.getElementById('stack-name')?.focus();
    else if (mode === 'yaml' && yamlFieldErrors.length > 0) document.getElementById('yaml-field-errors')?.focus();
    else if (first) serviceFormRef.current?.focusFirstError();
    else if (formError) document.getElementById('create-error')?.focus();
  }, [focusRequest]);

  // The shared guard asks before the user navigates away from a form they have typed into.
  useEffect(() => setUnsavedChanges(isCreateDirty({ stackName, cards, stackExtra, yamlEdited })), [stackName, cards, stackExtra, yamlEdited]);
  useEffect(() => () => setUnsavedChanges(false), []);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      clearTimeout(checkTimer.current);
    };
  }, []);

  function setStackName(name: string) {
    stackNameRef.current = name;
    setStackNameState(name);
    // Only the initial card follows the stack name, until its name is edited.
    if (cardsRef.current[0]?.autoName) {
      formEditedRef.current = true;
      patchCard(cardsRef.current[0].id, () => ({ name }));
    }
  }

  function addCard() {
    if (pendingRef.current || browserRef.current || cardsRef.current.length >= MAX_SERVICES) return;
    const card: Card = { id: nextId.current++, name: '', autoName: false, form: emptyForm(), extra: {}, errors: {} };
    formEditedRef.current = true;
    setCards((current) => [...current, card]);
    setOpenId(card.id);
    requestFocus({ kind: 'element', id: `svc-${card.id}-name` });
  }

  function removeCard(id: number) {
    const current = cardsRef.current;
    if (pendingRef.current || browserRef.current || current.length === 1) return;
    const index = current.findIndex((card) => card.id === id);
    if (index === -1) return;
    formEditedRef.current = true;
    setOpenId((open) => (open === id ? current[index > 0 ? index - 1 : 1].id : open));
    setCards((list) => list.filter((card) => card.id !== id));
  }

  function setCardName(id: number, name: string) {
    formEditedRef.current = true;
    patchCard(id, () => ({ name, autoName: false }));
  }

  function setCardForm(id: number, form: ServiceForm) {
    formEditedRef.current = true;
    patchCard(id, (card) => {
      // Switching the source makes the other source's errors stale; any other edit keeps them. The image field is
      // "Image" only in image mode, so an image error from there is stale in the other modes; in build mode an
      // `image` error otherwise belongs to the visible Image tag field.
      if (form.source === card.form.source) return { form };
      const stale = (key: string) => {
        if (key === 'dockerfileText') return form.source !== 'dockerfile';
        if (key.startsWith('build.')) return form.source !== 'build';
        return key === 'image' && card.form.source === 'image';
      };
      const errors = Object.fromEntries(Object.entries(card.errors).filter(([key]) => !stale(key)));
      return { form, errors };
    });
  }

  function browse(cardId: number, mode: 'dir' | 'file', set: (path: string) => void, purpose: BrowsePurpose) {
    if (pendingRef.current) return;
    const cb =
      purpose === 'dockerfile'
        ? (path: string) => {
            // A Dockerfile inside the build context is stored relative to it, otherwise absolute.
            const raw = (cardsRef.current.find((card) => card.id === cardId)?.form.buildContext ?? '').trim();
            const base = raw.replace(/\/+$/, ''); // '' for the root context "/"
            set(raw && path.startsWith(`${base}/`) ? path.slice(base.length + 1) : path);
          }
        : set;
    setBrowser({ purpose, mode, cb });
  }

  function pick(path: string) {
    browserRef.current?.cb(path);
    setBrowser(null);
  }

  function focusFirstError() {
    const first = cardsRef.current.find((card) => errorCount(card) > 0);
    if (first && modeRef.current === 'form') setOpenId(first.id);
    requestFocus({ kind: 'errors' });
  }

  // --- YAML mode ---

  function checkYaml(text: string) {
    const result = yamlToStack(text, dockerfilesRef.current, MAX_SERVICES);
    setYamlError(result.ok ? null : { message: result.message, ...(result.line !== undefined ? { line: result.line } : {}) });
    setYamlEmpty(result.ok && result.cards.length === 0);
    // A top-level name: feeds the stack name field while that is empty, so paste-and-create needs no retyping.
    if (result.ok && result.name !== undefined && stackNameRef.current === '') {
      stackNameRef.current = result.name;
      setStackNameState(result.name);
    }
    return result;
  }

  function setYamlText(text: string) {
    yamlTextRef.current = text;
    setYamlTextState(text);
    setYamlEdited(text !== storedYamlRef.current);
    setYamlFieldErrors([]);
    // The old verdict about an empty document is stale once the text changes; Create re-parses on submit anyway.
    setYamlEmpty(false);
    clearTimeout(checkTimer.current);
    checkTimer.current = setTimeout(() => checkYaml(yamlTextRef.current), YAML_CHECK_DELAY_MS);
  }

  function setStackExtra(extra: Extra) {
    stackExtraRef.current = extra;
    setStackExtraState(extra);
  }

  function showYaml(text: string) {
    clearTimeout(checkTimer.current);
    yamlTextRef.current = text;
    setYamlTextState(text);
    storedYamlRef.current = text;
    formEditedRef.current = false;
    setYamlEdited(false);
    setYamlFieldErrors([]);
    setFormError('');
    checkYaml(text);
    modeRef.current = 'yaml';
    setModeState('yaml');
  }

  function switchToYaml() {
    const current = cardsRef.current;
    const untouched = !isCreateDirty({ stackName: stackNameRef.current, cards: current, stackExtra: stackExtraRef.current, yamlEdited: false });
    // The stored text is only restored while its own name: still agrees with the stack name field.
    const stored = storedYamlRef.current;
    const storedName = stored !== null ? yamlToStack(stored, dockerfilesRef.current, MAX_SERVICES) : null;
    const nameAgrees = !storedName || !storedName.ok || storedName.name === undefined || storedName.name === stackNameRef.current;
    const restore = stored !== null && !formEditedRef.current && nameAgrees ? stored : null;
    if (restore === null && !untouched) {
      // An empty or repeated service name would produce a document that cannot be read back, so the name error is
      // shown on its card first. An untouched screen has nothing to convert and skips the check.
      const seen = new Set<string>();
      const blocked: number[] = [];
      const checked = current.map((card) => {
        const nameError =
          validateForm(card.form, { name: card.name, requireName: true, original: null }).name ?? (seen.has(card.name) ? `duplicate service name ${card.name}` : '');
        seen.add(card.name);
        if (nameError) blocked.push(card.id);
        const { name: _stale, ...others } = card.errors;
        return { ...card, errors: nameError ? { ...others, name: nameError } : others };
      });
      if (blocked.length > 0) {
        setCards(() => checked);
        setOpenId(blocked[0]);
        requestFocus({ kind: 'element', id: `svc-${blocked[0]}-name` });
        return;
      }
    }
    for (const card of current) if (card.form.source === 'dockerfile') dockerfilesRef.current[card.name] = card.form.dockerfileText;
    showYaml(restore ?? (untouched ? EMPTY_DOCUMENT : stackToYaml(current.map(({ name, form, extra }) => ({ name, form, extra })), stackExtraRef.current)));
  }

  function switchToForm() {
    clearTimeout(checkTimer.current);
    const result = checkYaml(yamlTextRef.current);
    if (!result.ok) return;
    const idsByName = new Map(cardsRef.current.map((card) => [card.name, card.id]));
    const rebuilt: Card[] = result.cards.map((card) => ({
      id: idsByName.get(card.name) ?? nextId.current++,
      name: card.name,
      autoName: false,
      form: card.form,
      extra: card.extra,
      errors: {},
    }));
    if (result.name !== undefined && stackNameRef.current === '') {
      stackNameRef.current = result.name;
      setStackNameState(result.name);
    }
    setStackExtra(result.extra);
    setCards(() => rebuilt);
    setOpenId((open) => (rebuilt.some((card) => card.id === open) ? open : (rebuilt[0]?.id ?? -1)));
    storedYamlRef.current = yamlTextRef.current;
    formEditedRef.current = false;
    setYamlEdited(false);
    setYamlFieldErrors([]);
    setFormError('');
    modeRef.current = 'form';
    setModeState('form');
  }

  function setMode(next: EditorMode) {
    if (pendingRef.current || browserRef.current || next === modeRef.current) return;
    if (next === 'yaml') switchToYaml();
    else switchToForm();
  }

  function sourceChange(cardId: number, source: ServiceForm['source']): ServiceForm | void {
    const card = cardsRef.current.find((candidate) => candidate.id === cardId);
    const stack = stackNameRef.current.trim();
    const service = card?.name.trim() ?? '';
    // Building replaces the image, so a pulled image's name must not become the built image's tag.
    if (!card || source !== 'dockerfile' || (card.form.source !== 'image' && card.form.image.trim() !== '') || !stack || !service) return;
    return { ...card.form, image: defaultImageTag(stack, service) };
  }

  const createBlocked =
    mode === 'yaml' ? (yamlError ? YAML_BLOCKED_REASON : yamlEmpty ? NO_SERVICE_REASON : '') : cards.length === 0 ? NO_SERVICE_REASON : '';

  interface Submitted {
    id: number;
    name: string;
    form: ServiceForm;
    extra: Extra;
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (pendingRef.current || browserRef.current) return;
    setUnavailable('');
    setFormError('');
    setYamlFieldErrors([]);
    let name = stackNameRef.current;
    const inYaml = modeRef.current === 'yaml';
    let sending: Submitted[];
    let sendingExtra: Extra;
    if (inYaml) {
      // YAML mode sends what the text says right now, not what the last debounced check saw.
      clearTimeout(checkTimer.current);
      const result = checkYaml(yamlTextRef.current);
      if (!result.ok || result.cards.length === 0) return;
      name = stackNameRef.current; // checkYaml may have taken it from a top-level name:
      sending = result.cards.map((card, index) => ({ id: -1 - index, name: card.name, form: card.form, extra: card.extra }));
      sendingExtra = result.extra;
      const nameError = validateForm(sending[0].form, { name, requireName: true, original: null }).name ?? '';
      setStackNameError(nameError);
      const lines = sending.flatMap((card) =>
        Object.entries(validateForm(card.form, { name: card.name, requireName: true, original: null })).map(
          ([path, message]) => `${card.name}: ${fieldLabel(path, card.form.source)}: ${message}`,
        ),
      );
      if (nameError || lines.length > 0) {
        setYamlFieldErrors(lines);
        requestFocus({ kind: 'errors' });
        return;
      }
    } else {
      const current = cardsRef.current;
      if (current.length === 0) return;
      const nameError = validateForm(current[0].form, { name, requireName: true, original: null }).name ?? '';
      setStackNameError(nameError);
      const names = new Set<string>();
      setCards((list) =>
        list.map((card) => {
          const errors = validateForm(card.form, { name: card.name, requireName: true, original: null });
          if (names.has(card.name)) errors.name = `duplicate service name ${card.name}`;
          names.add(card.name);
          return { ...card, errors };
        }),
      );
      if (nameError || cardsRef.current.some((card) => errorCount(card) > 0)) {
        focusFirstError();
        return;
      }
      sending = cardsRef.current;
      sendingExtra = stackExtraRef.current;
    }
    const body: CreateStackRequest = {
      name,
      services: sending.map((card): CreateStackService => {
        const writesDockerfile = card.form.source === 'dockerfile' && !card.form.readOnly.has('image') && !card.form.readOnly.has('build');
        return {
          service: card.name,
          config: fromForm(card.form, card.form.readOnly.size > 0 ? { limits: {} } : null, card.name),
          ...(Object.keys(card.extra).length > 0 ? { extra: card.extra } : {}),
          ...(writesDockerfile ? { dockerfile: { content: card.form.dockerfileText } } : {}),
        };
      }),
      ...(Object.keys(sendingExtra).length > 0 ? { extra: sendingExtra } : {}),
    };
    setPending(true);
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
      if (startNowRef.current) {
        try {
          const op = await api.act(data.name, 'up');
          trackOperation(op);
          refreshRunningOps();
          query += `&op=${encodeURIComponent(op.id)}`;
        } catch (err) {
          toasts.push({ kind: 'danger', message: `Stack created, but starting it failed: ${err instanceof Error ? err.message : String(err)}` });
        }
      }
      // The user may have discarded and left while the request was in flight; do not pull them back.
      if (!mountedRef.current) return;
      setUnsavedChanges(false);
      navigate(href(`/stacks/${encodeURIComponent(data.name)}?${query}`));
    } catch (err) {
      let nextFormError = '';
      let nextStackNameError = '';
      const lines: string[] = [];
      if (!(err instanceof ApiError)) {
        nextFormError = 'Could not reach the server. Check your connection and try again.';
      } else if (err.status === 400) {
        const { cards: grouped, rest } = groupServiceErrors(err.fields);
        const messages: string[] = [];
        for (const [index, fields] of grouped) {
          const sent = sending[index];
          const card = inYaml ? undefined : cardsRef.current.find((candidate) => candidate.id === sent?.id);
          if (!sent || (!inYaml && !card)) {
            messages.push(...fields.map((field) => field.message));
            continue;
          }
          const mapped = mapServerErrors(fields, sent.form);
          if (inYaml) {
            for (const [path, message] of Object.entries(mapped)) {
              lines.push(path === '_form' ? `${sent.name}: ${message}` : `${sent.name}: ${fieldLabel(path, sent.form.source)}: ${message}`);
            }
            continue;
          }
          if (mapped._form) messages.push(mapped._form);
          delete mapped._form;
          patchCard(sent.id, () => ({ errors: mapped }));
        }
        for (const field of rest) {
          if (field.field === 'name') nextStackNameError = nextStackNameError ? `${nextStackNameError}; ${field.message}` : field.message;
          else messages.push(field.message);
        }
        nextFormError = err.fields.length === 0 ? err.message : messages.join('\n');
      } else if (err.status === 409) {
        nextStackNameError = err.message;
      } else if (err.status === 503) {
        setUnavailable(err.message);
      } else {
        nextFormError = err.message;
      }
      setFormError(nextFormError);
      setStackNameError(nextStackNameError);
      setYamlFieldErrors(lines);
      focusFirstError();
    } finally {
      setPending(false);
    }
  }

  return {
    stackName,
    setStackName,
    cards,
    openId,
    open: setOpenId,
    addCard,
    removeCard,
    setCardName,
    setCardForm,
    mode,
    setMode,
    yamlText,
    setYamlText,
    yamlError,
    yamlFieldErrors,
    stackExtra,
    createBlocked,
    sourceChange,
    startNow,
    setStartNow: (value) => {
      startNowRef.current = value;
      setStartNow(value);
    },
    pending,
    formError,
    stackNameError,
    unavailable,
    browser,
    browse,
    pick,
    closeBrowser: () => setBrowser(null),
    errorCount,
    submit,
    focusFirstError,
    serviceFormRef,
  };
}
