import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { initAppearance } from '$lib/stores/appearance';
import { App } from './App';

const target = document.getElementById('app');
if (!target) throw new Error('missing #app mount point');

initAppearance();

createRoot(target).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
