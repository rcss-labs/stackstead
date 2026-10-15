import { mount } from 'svelte';
import './app.css';
import App from './App.svelte';
import { initAppearance } from './lib/stores/appearance';

const target = document.getElementById('app');
if (!target) throw new Error('missing #app mount point');

initAppearance();

export default mount(App, { target });
