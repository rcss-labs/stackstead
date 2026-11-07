import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { get } from '$lib/store';
import { palette } from '$lib/stores/palette';
import { afterEach, describe, expect, it } from 'vitest';
import NotFound from './NotFound';

afterEach(() => {
  cleanup();
  palette.set(false);
});

describe('Page not found', () => {
  it('names the page with an h1, which route focus waits for', () => {
    render(
      <MemoryRouter>
        <NotFound />
      </MemoryRouter>,
    );
    expect(screen.getByRole('heading', { level: 1, name: 'Page not found' })).toBeTruthy();
  });

  it('not-found offers Overview and search', () => {
    render(
      <MemoryRouter>
        <NotFound />
      </MemoryRouter>,
    );
    expect(screen.getByRole('link', { name: 'Go to Overview' }).getAttribute('href')).toBe('/');
    expect(get(palette)).toBe(false);
    fireEvent.click(screen.getByRole('button', { name: 'Search' }));
    expect(get(palette)).toBe(true);
  });
});
