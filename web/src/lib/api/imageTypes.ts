// Image library wire types. Mirrors internal/apitypes/images.go 1:1; change both together.
import type { Operation } from './types';

export interface Retention {
  /** 0 = unlimited. */
  keepLast: number;
  /** 0 = unlimited. */
  keepDays: number;
}

export interface GitHubSource {
  /** "owner/name". */
  repo: string;
  branch: string;
  /** Empty means the repository root. */
  path: string;
}

export interface LibraryServer {
  id: string;
  name: string;
  slug: string;
  local: boolean;
  status: 'reachable' | 'unreachable' | 'old';
  error?: string;
}

export interface PushTarget {
  id: string;
  registryId: string;
  registryName: string;
  kind: string;
  repository: string;
  autoPush: boolean;
}

export interface PushState {
  target: string;
  registry: string;
  repository: string;
  status: string;
  digest: string;
  error: string;
  updatedAt: string;
}

export interface LibraryVersion {
  id: string;
  tag: string;
  status: string;
  revision: number;
  commit: string;
  buildArgs: Record<string, string>;
  arch: string;
  imageId: string;
  size: number;
  startedAt: string;
  endedAt?: string;
  error: string;
  operationId: string;
  ref: string;
  pushes: PushState[];
}

export interface LibraryImage {
  slug: string;
  name: string;
  source: string;
  dockerfilePath: string;
  owner: LibraryServer;
  github?: GitHubSource;
  buildArgs: Record<string, string>;
  retention: Retention;
  latestVersion?: LibraryVersion;
  lastBuild?: LibraryVersion;
  pushTargets: PushTarget[];
  createdAt: string;
  updatedAt: string;
}

export interface Library {
  images: LibraryImage[];
  servers: LibraryServer[];
}

export interface LibraryImageDetail extends LibraryImage {
  versions: LibraryVersion[];
  suggestedTag: string;
  tagHint: string;
  arch: string;
  revision: number;
}

export interface LibraryUse {
  server: string;
  serverName: string;
  stack: string;
  service: string;
}

export interface LibraryVersionDetail extends LibraryVersion {
  log: string;
  usedBy: LibraryUse[];
}

export interface LibraryUsage {
  ref: string;
  stack: string;
  service: string;
}

export interface ImageFile {
  path: string;
  size: number;
}

export interface ImageFiles {
  revision: number;
  files: ImageFile[];
}

export interface ImageFileContent {
  path: string;
  content: string;
  revision: number;
}

/** PUT body and GET answer for a Dockerfile. */
export interface ImageDockerfile {
  content: string;
  revision: number;
}

export interface ImageRevision {
  number: number;
  createdAt: string;
  summary: string;
  dockerfileChanged: boolean;
}

export interface ImageRevisionDetail extends ImageRevision {
  dockerfile: string;
  previous: string;
  files: ImageFile[];
}

export interface CreateImageRequest {
  name: string;
  source: string;
  dockerfilePath: string;
  github?: GitHubSource;
  buildArgs: Record<string, string>;
  retention?: Retention;
}

export interface UpdateImageRequest {
  name: string;
  dockerfilePath: string;
  github?: GitHubSource;
  buildArgs: Record<string, string>;
  retention: Retention;
}

export interface RenameFileRequest {
  from: string;
  to: string;
}

export interface BuildRequest {
  tag: string;
  buildArgs: Record<string, string>;
  saveArgs: boolean;
  ref: string;
}

export interface BuildStarted {
  version: LibraryVersion;
  operation: Operation;
}

export interface PushRequest {
  target: string;
}

export interface PushTargetRequest {
  registryId: string;
  repository: string;
  autoPush: boolean;
}

export interface FetchRequest {
  ref: string;
}

/** Library badge on a service whose image is a library reference. */
export interface ServiceLibrary {
  ref: string;
  owner: string;
  ownerName: string;
  image: string;
  tag: string;
  latestTag: string;
  arch: string;
  newer: boolean;
  known: boolean;
}

export interface GitHubHead {
  commit: string;
  dockerfile: string;
  path: string;
}

export interface GitHubSettings {
  configured: boolean;
  login: string;
}

export interface GitHubTokenRequest {
  token: string;
}

export interface GitHubTest {
  ok: boolean;
  login: string;
  message: string;
}

/** Token is write-only: answers never include it. */
export interface RegistryHub {
  username: string;
  token: string;
}

/** Secrets are write-only: answers never include them. */
export interface RegistryECR {
  region: string;
  mode: string;
  accessKeyId: string;
  secretAccessKey: string;
  sessionToken: string;
  profile: string;
}

export interface RegistryRequest {
  name: string;
  kind: string;
  hub?: RegistryHub;
  ecr?: RegistryECR;
}

export interface RegistryView {
  id: string;
  name: string;
  kind: string;
  host: string;
  username: string;
  region: string;
  mode: string;
  profile: string;
  accountId: string;
  /** Non-secret half of static ECR keys; absent otherwise. */
  accessKeyId?: string;
  configured: boolean;
  lastTestAt?: string;
  lastTestOk: boolean;
  lastTestMessage: string;
}

/** The push-target picker's registry entry: no settings, no status. */
export interface RegistryOption {
  id: string;
  name: string;
  kind: string;
  host: string;
}

export interface RegistryTest {
  ok: boolean;
  host: string;
  message: string;
}

export interface ImageDefaults {
  retention: Retention;
}
