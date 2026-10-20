import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { FormEvent, RefObject } from 'react';
import { useNavigate } from 'react-router';
import { ApiError } from '$lib/api/client';
import { emptyForm, fromForm, groupServiceErrors, mapServerErrors, validateForm } from '$lib/compose/form';
import type { FormErrors, ServiceForm } from '$lib/compose/form';
import { isCreateDirty } from '$lib/stacks/createPreview';
import { setUnsavedChanges } from '$lib/stores/guard';
import { toasts } from '$lib/stores/toasts';
import type { BrowsePurpose, ServiceFormHandle } from '../stacks/ServiceForm';

import { useServer } from '../servers/ServerContext';
export const MAX_SERVICES = 20;

export interface Card {
  id: number;
  name: string;
  autoName: boolean;
  form: ServiceForm;
  errors: FormErrors;
}

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
  const [cards, setCardsState] = useState<Card[]>(() => [{ id: 0, name: '', autoName: true, form: emptyForm(), errors: {} }]);
  const cardsRef = useRef(cards);
  const [openId, setOpenId] = useState(0);
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
    else if (first) serviceFormRef.current?.focusFirstError();
    else if (formError) document.getElementById('create-error')?.focus();
  }, [focusRequest]);

  // The shared guard asks before the user navigates away from a form they have typed into.
  useEffect(() => setUnsavedChanges(isCreateDirty({ stackName, cards })), [stackName, cards]);
  useEffect(() => () => setUnsavedChanges(false), []);
  const mountedRef = useRef(true);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  function setStackName(name: string) {
    stackNameRef.current = name;
    setStackNameState(name);
    // Only the initial card follows the stack name, until its name is edited.
    if (cardsRef.current[0]?.autoName) patchCard(cardsRef.current[0].id, () => ({ name }));
  }

  function addCard() {
    if (pendingRef.current || browserRef.current || cardsRef.current.length >= MAX_SERVICES) return;
    const card: Card = { id: nextId.current++, name: '', autoName: false, form: emptyForm(), errors: {} };
    setCards((current) => [...current, card]);
    setOpenId(card.id);
    requestFocus({ kind: 'element', id: `svc-${card.id}-name` });
  }

  function removeCard(id: number) {
    const current = cardsRef.current;
    if (pendingRef.current || browserRef.current || current.length === 1) return;
    const index = current.findIndex((card) => card.id === id);
    if (index === -1) return;
    setOpenId((open) => (open === id ? current[index > 0 ? index - 1 : 1].id : open));
    setCards((list) => list.filter((card) => card.id !== id));
  }

  function setCardName(id: number, name: string) {
    patchCard(id, () => ({ name, autoName: false }));
  }

  function setCardForm(id: number, form: ServiceForm) {
    patchCard(id, (card) => {
      // Switching the source makes the other source's errors stale; any other edit keeps them (in build mode an
      // `image` error belongs to the visible Image tag field).
      if (form.source === card.form.source) return { form };
      const stale = form.source === 'build' ? (key: string) => key === 'image' : (key: string) => key.startsWith('build.');
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
    if (first) setOpenId(first.id);
    requestFocus({ kind: 'errors' });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (pendingRef.current || browserRef.current) return;
    setUnavailable('');
    setFormError('');
    const name = stackNameRef.current;
    const current = cardsRef.current;
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
    const submitted = cardsRef.current.map((card) => card.id);
    const body = { name, services: cardsRef.current.map((card) => ({ service: card.name, config: fromForm(card.form, null) })) };
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
      if (!(err instanceof ApiError)) {
        nextFormError = 'Could not reach the server. Check your connection and try again.';
      } else if (err.status === 400) {
        const { cards: grouped, rest } = groupServiceErrors(err.fields);
        const messages: string[] = [];
        for (const [index, fields] of grouped) {
          const card = cardsRef.current.find((candidate) => candidate.id === submitted[index]);
          if (!card) {
            messages.push(...fields.map((field) => field.message));
            continue;
          }
          const mapped = mapServerErrors(fields, card.form);
          if (mapped._form) messages.push(mapped._form);
          delete mapped._form;
          patchCard(card.id, () => ({ errors: mapped }));
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
