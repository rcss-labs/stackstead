import { act, render, screen, waitFor } from '@testing-library/react';
import { useEffect, useRef, useState } from 'react';
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router';
import { describe, expect, it } from 'vitest';
import { useFocusOnNavigate } from './useFocusOnNavigate';

// React Router commits navigations as transitions, so an App-level lazy screen already has its h1 when the
// path changes. A screen can still render its heading later (after a fetch or a nested Suspense), which only
// a hook-level test can stage.
function LateHeading() {
  const [ready, setReady] = useState(false);
  useEffect(() => {
    const timer = setTimeout(() => setReady(true), 50);
    return () => clearTimeout(timer);
  }, []);
  return ready ? <h1>Late page</h1> : <p>Loading</p>;
}

function SelfFocusing() {
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => input.current?.focus(), []);
  return (
    <>
      <h1>Search</h1>
      <input ref={input} aria-label="Query" />
    </>
  );
}

function Frame() {
  const mainRef = useRef<HTMLElement>(null);
  const navigate = useNavigate();
  useFocusOnNavigate(mainRef);
  return (
    <>
      <button type="button" onClick={() => navigate('/late')}>
        Go
      </button>
      <button type="button" onClick={() => navigate('/search')}>
        Search
      </button>
      <main ref={mainRef}>
        <Routes>
          <Route path="/" element={<h1>Home</h1>} />
          <Route path="/late" element={<LateHeading />} />
          <Route path="/search" element={<SelfFocusing />} />
        </Routes>
      </main>
    </>
  );
}

describe('useFocusOnNavigate', () => {
  it('focuses a heading that renders after the path change, not main', async () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <Frame />
      </MemoryRouter>,
    );
    const go = screen.getByRole('button', { name: 'Go' });
    go.focus();
    act(() => go.click());
    const h1 = await screen.findByRole('heading', { level: 1, name: 'Late page' });
    await waitFor(() => expect(document.activeElement).toBe(h1));
  });

  it('keeps focus a new page placed inside main as it rendered', async () => {
    render(
      <MemoryRouter initialEntries={['/']}>
        <Frame />
      </MemoryRouter>,
    );
    const search = screen.getByRole('button', { name: 'Search' });
    search.focus();
    act(() => search.click());
    const input = await screen.findByRole('textbox', { name: 'Query' });
    await new Promise((r) => setTimeout(r, 50));
    expect(document.activeElement).toBe(input);
  });
});
