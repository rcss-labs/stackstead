import type { FormEvent, ReactNode } from 'react';
import styles from './Auth.module.css';

export interface AuthCardProps {
  /** Visually hidden lead-in of the heading, e.g. "Sign in to". */
  verb: string;
  maxWidth: 360 | 400;
  children: ReactNode;
  onSubmit: (event: FormEvent<HTMLFormElement>) => void;
  noValidate?: boolean;
}

/** The brand card shared by the two public forms; App mounts them outside the shell. */
export function AuthCard({ verb, maxWidth, children, onSubmit, noValidate }: AuthCardProps) {
  return (
    <div className={styles.auth}>
      <form className={`${styles.card} ${styles.stack}`} style={{ maxWidth }} onSubmit={onSubmit} noValidate={noValidate}>
        <header className={styles.brand}>
          <img src="/logo-art.png" alt="" width={168} />
          <h1 className={styles.wordmark}>
            <span className="visually-hidden">{verb}</span> Stack<b>stead</b>
          </h1>
          <span className={styles.host}>{window.location.host}</span>
        </header>
        {children}
        <p className={styles.footer}>Localhost only. For remote access use a TLS reverse proxy.</p>
      </form>
    </div>
  );
}

/** Inline alert; shown only for a non-empty message, which never contains a typed credential. */
export function AuthError({ message }: { message: string }) {
  if (!message) return null;
  return (
    <p role="alert" className={styles.error}>
      <span aria-hidden="true">✖ </span>
      {message}
    </p>
  );
}

export function AuthHint({ children }: { children: ReactNode }) {
  return <p className={styles.hint}>{children}</p>;
}
