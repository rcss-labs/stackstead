import { describe, expect, it } from 'vitest';
import { dependsOn, neededBy } from './dependsOn';

const file = (...service: string[]) => ['services:', '  app:', ...service.map((l) => `    ${l}`), '  db:', '    image: postgres:16', ''].join('\n');
const deps = (...service: string[]) => dependsOn(file(...service), 'app');

describe('dependsOn', () => {
  it('reads the list form, with comments, quotes and blank lines', () => {
    expect(
      deps('image: x', 'depends_on:', '  # first the database', '  - db', '', '  - "broker" # queue', "  - 'cache'", 'restart: always'),
    ).toEqual(['db', 'broker', 'cache']);
  });

  it('reads a list whose dashes sit at the key indent', () => {
    expect(deps('depends_on:', '- db', '- cache', 'restart: always')).toEqual(['db', 'cache']);
  });

  it('reads the inline list, with quotes and a trailing comment', () => {
    expect(deps('depends_on: [db, "broker", \'cache\']  # needs these')).toEqual(['db', 'broker', 'cache']);
    expect(deps('depends_on: []')).toEqual([]);
  });

  it('reads the long map form and ignores each dependency\'s options', () => {
    expect(
      deps('depends_on:', '  db:', '    condition: service_healthy', '    restart: true', '  "broker":', '    condition: service_started', 'image: x'),
    ).toEqual(['db', 'broker']);
  });

  it('reads the inline map form, including long-form values', () => {
    expect(deps('depends_on: { db: { condition: service_healthy }, cache: {} }')).toEqual(['db', 'cache']);
  });

  it('answers an empty list when the service has no depends_on', () => {
    expect(deps('image: x')).toEqual([]);
  });

  it('ignores a depends_on key nested in another block of the service', () => {
    expect(deps('labels:', '  depends_on: db', 'environment:', '  depends_on: cache', 'image: x')).toEqual([]);
    expect(deps('labels:', '  depends_on: db', 'depends_on:', '  - broker')).toEqual(['broker']);
  });

  it('ignores a commented-out depends_on', () => {
    expect(deps('# depends_on: [db]', 'image: x')).toEqual([]);
  });

  it.each([
    ['an alias', ['depends_on: *shared']],
    ['an anchor', ['depends_on: &d [db]']],
    ['a merge key that may supply it', ['<<: *defaults', 'image: x']],
    ['extends, which may supply it', ['extends:', '  service: base']],
    ['a multi-line flow list', ['depends_on: [db,', '  cache]']],
    ['a block scalar', ['depends_on: |', '  db']],
    ['a tag', ['depends_on: !reset []']],
  ])('gives no answer for %s', (_name, lines) => {
    expect(deps(...lines)).toBeUndefined();
  });

  it('gives no answer when the service block cannot be found', () => {
    expect(dependsOn('services: {}\n', 'app')).toBeUndefined();
  });
});

describe('neededBy', () => {
  const text = ['services:', '  app:', '    depends_on: [db]', '  worker:', '    depends_on:', '      db:', '        condition: service_healthy', '  db:', '    image: p', ''].join('\n');

  it('lists the services that depend on one, in stack order', () => {
    expect(neededBy(text, 'db', ['app', 'worker', 'db'])).toEqual(['app', 'worker']);
    expect(neededBy(text, 'app', ['app', 'worker', 'db'])).toEqual([]);
  });

  it('gives no answer when any other service cannot be read reliably', () => {
    const odd = text.replace('depends_on: [db]', 'depends_on: *deps');
    expect(neededBy(odd, 'db', ['app', 'worker', 'db'])).toBeUndefined();
  });
});
