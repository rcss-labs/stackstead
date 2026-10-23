/** True when a latest tag is known and differs from the one in use. */
export function isNewerAvailable(current: string, latest: string | undefined): boolean {
  return latest !== undefined && latest !== '' && latest !== current;
}
