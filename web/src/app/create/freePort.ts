const MAX_PORT = 65535;

/** The first port above `after` that is not in `taken`, or undefined when none is left up to 65535. */
export function firstFreePortAfter(taken: ReadonlySet<number>, after: number): number | undefined {
  for (let port = after + 1; port <= MAX_PORT; port++) {
    if (!taken.has(port)) return port;
  }
  return undefined;
}
