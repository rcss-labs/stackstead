import type { ServiceConfig } from '$lib/api/types';

const indentOf = (line: string) => line.length - line.trimStart().length;
const isBlank = (line: string) => line.trim() === '';
const isComment = (line: string) => line.trimStart().startsWith('#');
const escapeRegExp = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/** Slices `<service>:` and its nested lines out of the raw compose text, or undefined when it cannot be found safely. */
export function serviceBlock(compose: string, service: string): string | undefined {
  const lines = compose.split('\n');
  const servicesAt = lines.findIndex((l) => /^services:\s*(#.*)?$/.test(l));
  if (servicesAt < 0) return undefined;

  // Lines of the services mapping: everything until the next top-level key.
  let end = lines.length;
  for (let i = servicesAt + 1; i < lines.length; i++) {
    const line = lines[i]!;
    if (!isBlank(line) && !isComment(line) && indentOf(line) === 0) {
      end = i;
      break;
    }
  }
  const body = lines.slice(servicesAt + 1, end);
  const firstKey = body.find((l) => !isBlank(l) && !isComment(l));
  if (firstKey === undefined || /^\s*\t/.test(firstKey)) return undefined;
  const keyIndent = indentOf(firstKey);

  const keyPattern = new RegExp(`^ {${keyIndent}}(["']?)${escapeRegExp(service)}\\1:(\\s.*)?$`);
  const start = body.findIndex((l) => keyPattern.test(l));
  if (start < 0) return undefined;

  let stop = body.length;
  for (let i = start + 1; i < body.length; i++) {
    const line = body[i]!;
    if (!isBlank(line) && indentOf(line) <= keyIndent) {
      stop = i;
      break;
    }
  }
  const block = body.slice(start, stop);
  while (block.length > 1 && isBlank(block[block.length - 1]!)) block.pop();
  return block.join('\n');
}

const RESERVED_WORDS = /^(true|false|null|yes|no|on|off|~)$/i;

function scalar(value: string): string {
  const needsQuotes =
    value === '' ||
    value !== value.trim() ||
    RESERVED_WORDS.test(value) ||
    /^[+-]?(\d[\d_]*)?(\.\d+)?([eE][+-]?\d+)?$/.test(value) ||
    /^\d+(:\d+)+$/.test(value) ||
    /: |\s#|:$/.test(value) ||
    /^[-?:,[\]{}#&*!|>'"%@`]/.test(value);
  return needsQuotes ? JSON.stringify(value) : value;
}

/** Deterministic compose-style YAML for one service, used when its block cannot be sliced from the file. */
export function serviceConfigYaml(name: string, config: ServiceConfig): string {
  const out: string[] = [`${name}:`];
  const add = (depth: number, text: string) => out.push(`${'  '.repeat(depth)}${text}`);

  if (config.image) add(1, `image: ${scalar(config.image)}`);
  if (config.build) {
    add(1, 'build:');
    add(2, `context: ${scalar(config.build.context)}`);
    if (config.build.dockerfile) add(2, `dockerfile: ${scalar(config.build.dockerfile)}`);
  }
  if (config.ports?.length) {
    add(1, 'ports:');
    for (const port of config.ports) add(2, `- ${JSON.stringify(port)}`);
  }
  if (config.volumes?.length) {
    add(1, 'volumes:');
    for (const volume of config.volumes) add(2, `- ${scalar(volume)}`);
  }
  if (config.environment?.length) {
    add(1, 'environment:');
    for (const { name: key, value } of config.environment) add(2, `${key}: ${scalar(value)}`);
  }
  if (config.networks?.length) {
    add(1, 'networks:');
    if (config.networks.some((n) => n.ipv4Address)) {
      for (const net of config.networks) {
        if (net.ipv4Address) {
          add(2, `${net.name}:`);
          add(3, `ipv4_address: ${scalar(net.ipv4Address)}`);
        } else add(2, `${net.name}: {}`);
      }
    } else for (const net of config.networks) add(2, `- ${net.name}`);
  }
  if (config.networkMode) add(1, `network_mode: ${scalar(config.networkMode)}`);
  if (config.limits.cpus || config.limits.memory) {
    add(1, 'deploy:');
    add(2, 'resources:');
    add(3, 'limits:');
    if (config.limits.cpus) add(4, `cpus: ${scalar(config.limits.cpus)}`);
    if (config.limits.memory) add(4, `memory: ${scalar(config.limits.memory)}`);
  }
  if (config.restart) add(1, `restart: ${scalar(config.restart)}`);
  const hc = config.healthcheck;
  if (hc && (hc.test?.length || hc.interval || hc.timeout || hc.startPeriod || hc.retries !== undefined)) {
    add(1, 'healthcheck:');
    if (hc.test?.length) add(2, `test: [${hc.test.map((t) => JSON.stringify(t)).join(', ')}]`);
    if (hc.interval) add(2, `interval: ${scalar(hc.interval)}`);
    if (hc.timeout) add(2, `timeout: ${scalar(hc.timeout)}`);
    if (hc.startPeriod) add(2, `start_period: ${scalar(hc.startPeriod)}`);
    if (hc.retries !== undefined) add(2, `retries: ${hc.retries}`);
  }
  return out.join('\n');
}

export function composeFileName(path: string | undefined): string {
  return path?.split('/').filter(Boolean).pop() ?? 'compose.yaml';
}

export function backupName(path: string | undefined): string {
  return path ? `${path.split('/').filter(Boolean).pop()}.bak` : 'a .bak file next to it';
}
