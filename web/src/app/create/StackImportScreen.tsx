import { useLayoutEffect, useRef, useState } from 'react';
import type { FormEvent } from 'react';
import { Link, useNavigate } from 'react-router';
import { ApiError } from '$lib/api/client';
import { toasts } from '$lib/stores/toasts';
import { Banner } from '../components/Banner';
import { Button } from '../components/Button';
import { Field } from '../components/Field';
import { FileBrowser } from '../stacks/FileBrowser';
import styles from './StackImportScreen.module.css';

import { useServer } from '../servers/ServerContext';
export function StackImportScreen() {
  const navigate = useNavigate();
  const { api, refreshRunningOps, refreshStacks, href } = useServer();
  const [path, setPath] = useState('');
  const [pathError, setPathError] = useState('');
  const [composeError, setComposeError] = useState('');
  const [unavailable, setUnavailable] = useState('');
  const [pending, setPending] = useState(false);
  const [browsing, setBrowsing] = useState(false);

  // Focus is requested while handling the submit and applied once the errors it set are in the DOM
  // (the React equivalent of `await tick()`).
  const pendingFocus = useRef(false);
  const [focusRequest, setFocusRequest] = useState(0);
  const pendingRef = useRef(false);

  function requestFocus() {
    pendingFocus.current = true;
    setFocusRequest((n) => n + 1);
  }

  // composeError is read at the render a focus request commits with, so [focusRequest] is the only dep that matters.
  useLayoutEffect(() => {
    if (!pendingFocus.current) return;
    pendingFocus.current = false;
    const target = composeError ? document.getElementById('import-compose-error') : document.getElementById('import-path');
    target?.focus();
  }, [focusRequest]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (pendingRef.current) return;
    setPathError('');
    setComposeError('');
    setUnavailable('');
    const trimmed = path.trim();
    if (!trimmed) {
      setPathError('Enter the absolute path of a compose file.');
      requestFocus();
      return;
    }
    pendingRef.current = true;
    setPending(true);
    try {
      const { data, checksPartial } = await api.importStack(trimmed);
      void refreshStacks();
      refreshRunningOps();
      const notes: string[] = [];
      if (checksPartial) notes.push('port/IP conflict checks could not run (Docker unreachable)');
      if (data.warnings?.length) notes.push(`other local users can modify the compose file: ${data.warnings.join('; ')}`);
      toasts.push({
        kind: notes.length > 0 ? 'warning' : 'success',
        message: notes.length > 0 ? `Stack ${data.name} imported, but ${notes.join(', and ')}.` : `Stack ${data.name} imported`,
      });
      navigate(href(`/stacks/${encodeURIComponent(data.name)}`));
    } catch (err) {
      if (!(err instanceof ApiError)) setPathError('Could not reach the server. Check your connection and try again.');
      else if (err.status === 422) setComposeError(err.message);
      else if (err.status === 503) setUnavailable(err.message);
      else if (err.status === 400 && err.fields.length > 0) setPathError(err.fields.map((f) => f.message).join('; '));
      else setPathError(err.message);
      requestFocus();
    } finally {
      pendingRef.current = false;
      setPending(false);
    }
  }

  return (
    <section className={styles.page}>
      <h1>Import stack</h1>
      <p className={styles.lead}>Register a compose file where it already lives. Nothing is moved or changed until you edit it.</p>
      {unavailable && (
        <Banner kind="danger" title="Docker/compose unavailable: the stack was not imported">
          {unavailable}
        </Banner>
      )}
      <form className={styles.card} noValidate onSubmit={submit}>
        <div className={styles.body}>
          <Field
            label="Compose file path"
            id="import-path"
            mono
            hint="Absolute path. Browse lists the allowed directories only; the path may also point outside them."
            error={pathError}
          >
            {({ readOnly: _readOnly, ...aria }) => (
              <div className={styles.inline}>
                <input {...aria} type="text" value={path} onChange={(e) => setPath(e.target.value)} autoComplete="off" spellCheck={false} autoCapitalize="off" />
                <Button variant="secondary" aria-label="Browse for compose file" className={styles.browse} onClick={() => setBrowsing(true)}>
                  Browse…
                </Button>
              </div>
            )}
          </Field>
          {composeError && (
            <div id="import-compose-error" className={styles.composeError} role="alert" tabIndex={-1}>
              <div className={styles.head}>
                <span className={styles.what}>Compose file</span>
                <span className={styles.verdict}>cannot import</span>
              </div>
              <pre>{composeError}</pre>
            </div>
          )}
        </div>
        <footer className={styles.footer}>
          <Link to={href('/')} className={styles.cancel}>
            Cancel
          </Link>
          <Button variant="primary" type="submit" className={styles.submit} loading={pending}>
            Import stack
          </Button>
        </footer>
      </form>
      <FileBrowser
        open={browsing}
        mode="file"
        title="Choose a compose file"
        filter={(e) => /\.ya?ml$/.test(e.name)}
        onPick={(p) => {
          setPath(p);
          setBrowsing(false);
        }}
        onClose={() => setBrowsing(false)}
      />
    </section>
  );
}
