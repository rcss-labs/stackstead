// Whether an editor holds unsaved changes. App reads it to confirm before a
// link navigates away; StackDetail owns it and clears it when editors go away.
import { writable, type Readable } from '$lib/store';

const dirty = writable(false);
export const unsavedChanges: Readable<boolean> = { subscribe: dirty.subscribe };
export function setUnsavedChanges(value: boolean): void {
  dirty.set(value);
}
