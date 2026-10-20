import { describe, expect, it } from 'vitest';
import { backupName, composeFileName, dedent, serviceBlock, serviceConfigYaml } from './composeText';

const compose = `name: demo
services:
  app:
    # the web app
    image: nginx:1.25
    ports:
      - "8080:80"

  db:
    image: postgres:16
volumes:
  data: {}
`;

describe('serviceBlock', () => {
  it('slices a service block with its nested lines and comments', () => {
    expect(serviceBlock(compose, 'app')).toBe(
      ['  app:', '    # the web app', '    image: nginx:1.25', '    ports:', '      - "8080:80"'].join('\n'),
    );
    expect(serviceBlock(compose, 'db')).toBe(['  db:', '    image: postgres:16'].join('\n'));
  });

  it('returns undefined when services or the key is missing', () => {
    expect(serviceBlock('name: x\n', 'app')).toBeUndefined();
    expect(serviceBlock(compose, 'cache')).toBeUndefined();
    expect(serviceBlock('services:\n\tapp:\n\t\timage: x\n', 'app')).toBeUndefined();
  });

  it('stops at the next sibling and ignores a same-named key under another mapping', () => {
    const text = `x-meta:
  app:
    image: wrong
services:
  web:
    image: a
    app:
      nested: true
  app:
    image: right
`;
    expect(serviceBlock(text, 'app')).toBe('  app:\n    image: right');
    expect(serviceBlock(text, 'web')).toBe('  web:\n    image: a\n    app:\n      nested: true');
  });
});

describe('serviceBlock comments and later mappings', () => {
  it('keeps lines after an interior column-0 comment and drops comments of the next sibling', () => {
    const text = `services:
  app:
    image: x
#    ports:
    environment:
      A: b
# about db
  db:
    image: y
`;
    expect(serviceBlock(text, 'app')).toBe('  app:\n    image: x\n#    ports:\n    environment:\n      A: b');
  });

  it('does not find a key under a later top-level mapping', () => {
    expect(serviceBlock(compose, 'data')).toBeUndefined();
  });
});

describe('serviceConfigYaml', () => {
  it('formats a config as YAML in a fixed order', () => {
    const yaml = serviceConfigYaml('app', {
      image: 'nginx:1.25',
      ports: ['8080:80'],
      environment: [{ name: 'MODE', value: 'prod' }],
      limits: { cpus: '0.5', memory: '512m' },
      restart: 'unless-stopped',
      healthcheck: { test: ['CMD', 'curl', '-f', 'http://localhost'], interval: '30s', retries: 3 },
    });
    expect(yaml).toBe(
      [
        'app:',
        '  image: nginx:1.25',
        '  ports:',
        '    - "8080:80"',
        '  environment:',
        '    MODE: prod',
        '  deploy:',
        '    resources:',
        '      limits:',
        '        cpus: "0.5"',
        '        memory: 512m',
        '  restart: unless-stopped',
        '  healthcheck:',
        '    test: ["CMD", "curl", "-f", "http://localhost"]',
        '    interval: 30s',
        '    retries: 3',
      ].join('\n'),
    );
  });

  it('formats build, networks and network_mode', () => {
    const yaml = serviceConfigYaml('api', {
      build: { context: '.', dockerfile: 'Dockerfile.dev' },
      networks: [{ name: 'front', ipv4Address: '10.0.0.5' }, { name: 'back' }],
      networkMode: 'host',
      limits: {},
    });
    expect(yaml).toBe(
      [
        'api:',
        '  build:',
        '    context: .',
        '    dockerfile: Dockerfile.dev',
        '  networks:',
        '    front:',
        '      ipv4_address: 10.0.0.5',
        '    back: {}',
        '  network_mode: host',
      ].join('\n'),
    );
    expect(serviceConfigYaml('a', { networks: [{ name: 'n' }], limits: {} })).toBe('a:\n  networks:\n    - n');
  });

  it('omits empty limits and missing keys', () => {
    expect(serviceConfigYaml('app', { image: 'x', limits: {} })).toBe('app:\n  image: x');
    expect(serviceConfigYaml('app', { limits: { memory: '1g' } })).toBe('app:\n  deploy:\n    resources:\n      limits:\n        memory: 1g');
  });
});

describe('file names', () => {
  it('file and backup names fall back', () => {
    expect(composeFileName(undefined)).toBe('compose.yaml');
    expect(composeFileName('/srv/web/docker-compose.yml')).toBe('docker-compose.yml');
    expect(backupName('/srv/web/docker-compose.yml')).toBe('docker-compose.yml.bak');
    expect(backupName(undefined)).toBe('a .bak file next to it');
  });
});

describe('dedent', () => {
  it('strips the common leading indentation and keeps relative nesting and blank lines', () => {
    expect(dedent('  app:\n    image: x\n\n    # c\n    ports:\n      - "1:1"')).toBe('app:\n  image: x\n\n  # c\n  ports:\n    - "1:1"');
  });

  it('leaves text that already starts at column 0', () => {
    expect(dedent('app:\n  image: x')).toBe('app:\n  image: x');
  });
});
