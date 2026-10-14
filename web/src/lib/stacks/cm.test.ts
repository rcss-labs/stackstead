import { describe, expect, it, vi } from 'vitest';
import { createEditor } from './cm';

describe('createEditor (real CodeMirror in jsdom)', () => {
  it('mounts with the value, reports edits, supports setValue and destroy', () => {
    const parent = document.createElement('div');
    document.body.append(parent);
    const onchange = vi.fn();
    const h = createEditor({ parent, value: 'a: 1\n', readonly: false, ariaLabel: 'Compose file', onchange });
    expect(parent.querySelector('.cm-content')?.getAttribute('aria-label')).toBe('Compose file');
    expect(h.getValue()).toBe('a: 1\n');
    h.setValue('b: 2\n');
    expect(onchange).toHaveBeenCalledWith('b: 2\n');
    h.destroy();
    expect(parent.querySelector('.cm-editor')).toBeNull();
  });
});
