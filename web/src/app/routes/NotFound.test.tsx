import { cleanup, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it } from 'vitest';
import NotFound from './NotFound';

afterEach(cleanup);

describe('Page not found', () => {
  it('not-found offers Overview and search', () => {
    render(
      <MemoryRouter>
        <NotFound />
      </MemoryRouter>,
    );
    expect(screen.getByRole('link', { name: 'Go to Overview' }).getAttribute('href')).toBe('/');
  });
});
