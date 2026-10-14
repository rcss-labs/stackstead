import { render, screen } from '@testing-library/svelte';
import { describe, expect, it } from 'vitest';
import App from './App.svelte';

describe('App shell', () => {
  it('renders the heading and a main landmark', () => {
    render(App);
    expect(screen.getByRole('heading', { name: 'Stackstead' })).toBeTruthy();
    expect(screen.getByRole('main')).toBeTruthy();
  });
});
