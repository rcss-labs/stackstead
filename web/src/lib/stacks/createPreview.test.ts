import { describe, expect, it } from 'vitest';
import { emptyForm } from '../compose/form';
import { isCreateDirty, previewYaml } from './createPreview';

describe('previewYaml', () => {
  it('renders one image service', () => {
    expect(previewYaml([{ name: 'web', form: { ...emptyForm(), image: 'nginx:1.27' } }])).toBe('services:\n  web:\n    image: nginx:1.27');
  });

  it('renders cards in order with ports, volumes and build', () => {
    const yaml = previewYaml([
      { name: 'db', form: { ...emptyForm(), image: 'postgres:16', volumes: ['pgdata:/var/lib/postgresql/data'] } },
      {
        name: 'app',
        form: { ...emptyForm(), source: 'build', buildContext: '/srv/app', dockerfile: 'build.Dockerfile', ports: ['8080:80'] },
      },
    ]);
    expect(yaml).toBe(
      [
        'services:',
        '  db:',
        '    image: postgres:16',
        '    volumes:',
        '      - pgdata:/var/lib/postgresql/data',
        '  app:',
        '    build:',
        '      context: /srv/app',
        '      dockerfile: build.Dockerfile',
        '    ports:',
        '      - "8080:80"',
      ].join('\n'),
    );
  });

  it('falls back to service-<n> for an unnamed card and {} for an empty one', () => {
    expect(previewYaml([{ name: '', form: emptyForm() }])).toBe('services:\n  service-1: {}');
  });

  it('ignores blank rows', () => {
    expect(previewYaml([{ name: 'web', form: { ...emptyForm(), image: 'x', ports: ['', ' '] } }])).not.toContain('ports:');
  });
});

describe('isCreateDirty', () => {
  const clean = () => ({ stackName: '', cards: [{ name: '', autoName: true, form: emptyForm() }] });

  it('is clean for the initial state', () => {
    expect(isCreateDirty(clean())).toBe(false);
  });

  it('is dirty with a stack name', () => {
    expect(isCreateDirty({ ...clean(), stackName: 'shop' })).toBe(true);
  });

  it('is dirty with a second card', () => {
    const input = clean();
    input.cards.push({ name: '', autoName: true, form: emptyForm() });
    expect(isCreateDirty(input)).toBe(true);
  });

  it('is dirty once a form field changes', () => {
    const input = clean();
    input.cards[0].form.image = 'x';
    expect(isCreateDirty(input)).toBe(true);
  });

  it("is dirty once the first card's name is edited", () => {
    const input = clean();
    input.cards[0] = { ...input.cards[0], autoName: false, name: 'api' };
    expect(isCreateDirty(input)).toBe(true);
  });

  it('stays clean when the first card only mirrors the stack name', () => {
    const input = clean();
    input.cards[0] = { ...input.cards[0], autoName: true, name: 'web' };
    expect(isCreateDirty(input)).toBe(false);
  });
});
