import { cleanup, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it } from 'vitest';
import type { StackSummary } from '$lib/api/types';
import { localScope, ServerContext, type ServerScope } from '../servers/ServerContext';
import { StacksList } from './StacksList';

afterEach(cleanup);

const stack = (name: string, ports: string[]): StackSummary => ({
  name,
  path: `/srv/stacks/${name}/compose.yaml`,
  project: `proj-${name}`,
  origin: 'managed',
  status: 'running',
  serviceCount: 1,
  running: 1,
  health: { healthy: 0, unhealthy: 0, starting: 0, none: 0, failed: 0 },
  ports,
  libraryUpdates: 0,
});

function list(items: StackSummary[], extra: Partial<Parameters<typeof StacksList>[0]> = {}, scope: ServerScope = localScope) {
  render(
    <MemoryRouter>
      <ServerContext.Provider value={scope}>
        <StacksList
          items={items}
          alerts={undefined}
          summary={undefined}
          query=""
          onQuery={() => {}}
          runningStacks={new Set()}
          reasonFor={() => undefined}
          acting={new Set()}
          onAct={() => {}}
          {...extra}
        />
      </ServerContext.Provider>
    </MemoryRouter>,
  );
}
const rowOf = (name: string) => within(document.querySelector(`[data-stack="${name}"]`) as HTMLElement);

describe('Port links on rows from several servers', () => {
  it('links only ports of this server; a remote row with the same port shows text, not this host', () => {
    list([stack('here', ['8080']), stack('there', ['8080'])], {
      serverOf: (s) => (s.name === 'there' ? { name: 'Mini 02', slug: 'mini-02' } : { name: 'Here', slug: null }),
      keyOf: (s) => s.name,
    });
    expect(rowOf('here').getByRole('link', { name: ':8080' }).getAttribute('href')).toBe(`http://${window.location.hostname}:8080`);
    expect(rowOf('there').queryByRole('link', { name: ':8080' })).toBeNull();
    expect(rowOf('there').getByText(':8080')).toBeTruthy();
  });

  it("does not link ports when viewing another server's own list", () => {
    list([stack('web', ['8080'])], {}, { ...localScope, local: false, id: 'peer-1', slug: 'mini-02', name: 'Mini 02' });
    expect(screen.queryByRole('link', { name: ':8080' })).toBeNull();
    expect(screen.getByText(':8080')).toBeTruthy();
  });
});
