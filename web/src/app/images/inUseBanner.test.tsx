import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { ApiError } from '$lib/api/client';
import { InUseBanner, inUseFrom } from './InUseBanner';

const peer = { id: 'peer-1', name: 'Host B', slug: 'host-b', local: false, status: 'reachable' as const };

describe('InUseBanner', () => {
  it('labels uses on other servers and lists servers that could not be asked', () => {
    render(
      <InUseBanner
        title="Version in use"
        localId="me"
        inUse={{
          message: 'image is in use',
          usedBy: [
            { server: 'me', serverName: 'Here', stack: 'web', service: 'app' },
            { server: 'peer-1', serverName: 'Host B', stack: 'blog', service: 'site' },
          ],
          unreachable: [{ ...peer, id: 'peer-2', name: 'Host C', status: 'unreachable' }],
        }}
      />,
    );
    const banner = screen.getByRole('alert');
    expect(within(banner).getByText('web / app')).toBeTruthy();
    expect(within(banner).getByText('blog / site on Host B')).toBeTruthy();
    expect(within(banner).getByText('could not ask Host C')).toBeTruthy();
  });

  it('shows only the message when nothing is listed', () => {
    render(<InUseBanner title="Image in use" localId="me" inUse={{ message: 'image is in use', usedBy: [], unreachable: [] }} />);
    expect(screen.queryAllByRole('listitem')).toHaveLength(0);
  });
});

describe('inUseFrom', () => {
  const refusal = (body: unknown) => new ApiError(409, 'image is in use', [], body, 'image_in_use');

  it('reads usedBy and unreachable from the 409 body', () => {
    const parsed = inUseFrom(refusal({ usedBy: [], unreachable: [peer] }));
    expect(parsed?.unreachable).toEqual([peer]);
  });

  it('defaults both lists to empty for a body without them and ignores other errors', () => {
    expect(inUseFrom(refusal(undefined))).toEqual({ message: 'image is in use', usedBy: [], unreachable: [] });
    expect(inUseFrom(new ApiError(409, 'busy', [], {}, 'build_running'))).toBeUndefined();
  });
});
