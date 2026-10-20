import { renderHook } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { findRoute } from '../routes';
import { useDocumentTitle } from './useDocumentTitle';

describe('useDocumentTitle', () => {
  it('document title includes the server name', () => {
    renderHook(() => useDocumentTitle(findRoute('/s/mini-02/containers'), true, 'mini-02'));
    expect(document.title).toBe('Containers · mini-02 · Stackstead');
  });

  it('is unchanged for the local server', () => {
    renderHook(() => useDocumentTitle(findRoute('/networks'), true));
    expect(document.title).toBe('Networks · Stackstead');
  });

  it('does not title an unknown page on a server as an existing one', () => {
    expect(findRoute('/s/mini-02/settings')?.title).toBe('Page not found');
  });
});
