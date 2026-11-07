// Whether the command palette is open. Owned by App; screens that offer a Search button set it.
import { writable } from '$lib/store';

export const palette = writable(false);
