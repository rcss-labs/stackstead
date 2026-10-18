import type { UpdateStatus } from '../api/types';
import { relativeTime } from '../format';

export type Described = {
  pill: string;
  tone: 'success' | 'accent' | 'danger' | 'muted';
  headline: string;
  detail?: string;
};

/** Release notes are shown only as an ordinary https link on github.com; anything else is dropped. */
export function safeNotesURL(raw: string): string | undefined {
  try {
    const u = new URL(raw);
    return u.protocol === 'https:' && u.hostname === 'github.com' && !u.username && !u.password ? u.href : undefined;
  } catch {
    return undefined;
  }
}

const SENTENCES: Record<string, string> = {
  network: 'no route to github.com',
  timeout: 'the release source did not answer in time',
  http: 'the release source answered with an error',
  malformed: 'the release listing could not be read',
};

/** A readable sentence for a server error code; an unknown code never reaches the screen as a slug. */
export function errorSentence(code: string): string {
  return Object.hasOwn(SENTENCES, code) ? SENTENCES[code] : 'the check did not complete';
}

const withV = (version: string) => (version.startsWith('v') ? version : `v${version}`);
const isRelease = (version: string) => /^v?\d+\.\d+\.\d+$/.test(version);

export function describe(st: UpdateStatus, now: Date): Described {
  const checked = st.checkedAt ? relativeTime(st.checkedAt, now) : '';
  // A failed check after an earlier success keeps the earlier result and says so.
  const detailAfterSuccess = st.error
    ? `The last check failed (${errorSentence(st.error)}); showing the result from ${checked}.`
    : `Checked ${checked}.`;
  switch (st.state) {
    case 'up-to-date':
      return {
        pill: 'up to date',
        tone: 'success',
        headline: `Stackstead ${withV(st.current)} is the latest release.`,
        detail: detailAfterSuccess,
      };
    case 'available':
      return {
        pill: 'update available',
        tone: 'accent',
        headline: `${withV(st.current)} → ${withV(st.latest)}`,
        detail: detailAfterSuccess,
      };
    case 'failed':
      return {
        pill: 'check failed',
        tone: 'danger',
        headline: `Could not reach the release source (${errorSentence(st.error)}). Nothing is known about newer releases.`,
        detail: 'You can still upgrade from the terminal.',
      };
    case 'disabled':
      return { pill: 'checks off', tone: 'muted', headline: 'Update checks are disabled (update_check: false).' };
    default:
      if (isRelease(st.current)) return { pill: 'not checked', tone: 'muted', headline: 'Not checked yet.' };
      return {
        pill: 'dev build',
        tone: 'muted',
        headline: `This is a development build (${st.current}); versions cannot be compared.`,
      };
  }
}
