import type { LibraryServer, LibraryUse } from '$lib/api/imageTypes';
import { ApiError } from '$lib/api/client';
import { Banner } from '../components/Banner';
import styles from './images.module.css';

export interface InUse {
  message: string;
  usedBy: LibraryUse[];
  /** Servers that could not be asked, so the image may still be in use there. */
  unreachable: LibraryServer[];
}

/** The services a refused delete (409 image_in_use) lists, from the error body's `usedBy` and `unreachable`. */
export function inUseFrom(e: unknown): InUse | undefined {
  if (!(e instanceof ApiError) || e.status !== 409 || e.code !== 'image_in_use') return undefined;
  const body = e.body as { usedBy?: LibraryUse[]; unreachable?: LibraryServer[] } | undefined;
  return {
    message: e.message,
    usedBy: Array.isArray(body?.usedBy) ? body.usedBy : [],
    unreachable: Array.isArray(body?.unreachable) ? body.unreachable : [],
  };
}

/** `localId` is this server's id: uses on any other server are labelled "on <server name>". */
export function InUseBanner({ title, inUse, localId }: { title: string; inUse: InUse; localId?: string }) {
  const { message, usedBy, unreachable } = inUse;
  return (
    <Banner kind="danger" title={title}>
      <p>{message}</p>
      {(usedBy.length > 0 || unreachable.length > 0) && (
        <ul className={styles.usedBy}>
          {usedBy.map((use) => (
            <li key={`${use.server}/${use.stack}/${use.service}`}>
              {use.stack} / {use.service}
              {use.server !== localId && ` on ${use.serverName}`}
            </li>
          ))}
          {unreachable.map((server) => (
            <li key={`unreachable/${server.id}`}>could not ask {server.name}</li>
          ))}
        </ul>
      )}
    </Banner>
  );
}
