import { ApiError, notifyUnauthorized, request } from './client';
import type {
  BuildRequest,
  BuildStarted,
  CreateImageRequest,
  FetchRequest,
  GitHubHead,
  GitHubSettings,
  GitHubTest,
  GitHubTokenRequest,
  ImageDefaults,
  ImageDockerfile,
  ImageFileContent,
  ImageFiles,
  ImageRevision,
  ImageRevisionDetail,
  Library,
  LibraryImage,
  LibraryImageDetail,
  LibraryVersionDetail,
  PushRequest,
  PushTarget,
  PushTargetRequest,
  RegistryRequest,
  RegistryTest,
  RegistryView,
  RenameFileRequest,
  UpdateImageRequest,
} from './imageTypes';
import type { FieldError, Operation } from './types';

type Verb = 'GET' | 'POST' | 'PUT' | 'DELETE';

const seg = encodeURIComponent;
/** Encodes each path segment and keeps the slashes that separate them. */
const segments = (path: string) => path.split('/').map(seg).join('/');

/** Multipart POST: the browser sets Content-Type with the boundary, so none is passed here. */
async function upload<T>(path: string, form: FormData): Promise<T> {
  let res: Response;
  try {
    res = await fetch(path, {
      method: 'POST',
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
      redirect: 'error',
      body: form,
    });
  } catch {
    throw new ApiError(0, 'network error');
  }
  let parsed: unknown;
  try {
    parsed = await res.json();
  } catch {
    parsed = undefined;
  }
  if (!res.ok) {
    const b = (parsed && typeof parsed === 'object' ? parsed : {}) as { error?: unknown; fields?: unknown; code?: unknown };
    const message = typeof b.error === 'string' && b.error ? b.error : `HTTP ${res.status}`;
    const fields = Array.isArray(b.fields) ? (b.fields as FieldError[]) : [];
    const code = typeof b.code === 'string' && b.code ? b.code : undefined;
    if (res.status === 401) notifyUnauthorized(path);
    throw new ApiError(res.status, message, fields, parsed, code);
  }
  if (parsed === undefined) throw new ApiError(res.status, 'invalid response');
  return parsed as T;
}

/** Image library routes on a server; `base` is '' for this server or '/api/servers/{id}' for a connected one. */
export function createImagesApi(base: string) {
  const call = <T>(method: Verb, path: string, body?: unknown) =>
    request<T>(method, base + path, body).then((r) => r.data);

  return {
    images: () => call<Library>('GET', '/api/images'),
    createImage: (req: CreateImageRequest) => call<LibraryImage>('POST', '/api/images', req),
    image: (slug: string) => call<LibraryImageDetail>('GET', `/api/images/${seg(slug)}`),
    updateImage: (slug: string, req: UpdateImageRequest) =>
      call<LibraryImage>('PUT', `/api/images/${seg(slug)}`, req),
    deleteImage: (slug: string) => call<void>('DELETE', `/api/images/${seg(slug)}`),

    imageFiles: (slug: string) => call<ImageFiles>('GET', `/api/images/${seg(slug)}/files`),
    imageFile: (slug: string, path: string) =>
      call<ImageFileContent>('GET', `/api/images/${seg(slug)}/files/${segments(path)}`),
    uploadImageFiles: (slug: string, files: { path: string; blob: Blob }[]) => {
      const form = new FormData();
      for (const f of files) form.append(`f/${f.path}`, f.blob);
      return upload<ImageRevision>(`${base}/api/images/${seg(slug)}/files`, form);
    },
    renameImageFile: (slug: string, req: RenameFileRequest) =>
      call<ImageRevision>('POST', `/api/images/${seg(slug)}/files/rename`, req),
    deleteImageFile: (slug: string, path: string) =>
      call<ImageRevision>('DELETE', `/api/images/${seg(slug)}/files/${segments(path)}`),

    getImageDockerfile: (slug: string) => call<ImageDockerfile>('GET', `/api/images/${seg(slug)}/dockerfile`),
    saveImageDockerfile: (slug: string, content: string, revision: number) =>
      call<ImageRevision>('PUT', `/api/images/${seg(slug)}/dockerfile`, { content, revision } satisfies ImageDockerfile),
    imageRevisions: (slug: string) => call<ImageRevision[]>('GET', `/api/images/${seg(slug)}/revisions`),
    imageRevision: (slug: string, n: number) =>
      call<ImageRevisionDetail>('GET', `/api/images/${seg(slug)}/revisions/${n}`),
    restoreImageRevision: (slug: string, n: number) =>
      call<ImageRevision>('POST', `/api/images/${seg(slug)}/revisions/${n}/restore`),

    buildImage: (slug: string, req: BuildRequest) =>
      call<BuildStarted>('POST', `/api/images/${seg(slug)}/builds`, req),
    imageVersion: (slug: string, id: string) =>
      call<LibraryVersionDetail>('GET', `/api/images/${seg(slug)}/versions/${seg(id)}`),
    deleteImageVersion: (slug: string, id: string) =>
      call<void>('DELETE', `/api/images/${seg(slug)}/versions/${seg(id)}`),
    pushImageVersion: (slug: string, tag: string, target: string) =>
      call<Operation>('POST', `/api/images/${seg(slug)}/versions/${seg(tag)}/push`, { target } satisfies PushRequest),

    addPushTarget: (slug: string, req: PushTargetRequest) =>
      call<PushTarget>('POST', `/api/images/${seg(slug)}/push-targets`, req),
    updatePushTarget: (slug: string, id: string, req: PushTargetRequest) =>
      call<PushTarget>('PUT', `/api/images/${seg(slug)}/push-targets/${seg(id)}`, req),
    deletePushTarget: (slug: string, id: string) =>
      call<void>('DELETE', `/api/images/${seg(slug)}/push-targets/${seg(id)}`),

    imageRegistries: () => call<RegistryView[]>('GET', '/api/images/registries'),
    githubHead: (slug: string) => call<GitHubHead>('GET', `/api/images/${seg(slug)}/github/head`),
    fetchImage: (ref: string) => call<Operation>('POST', '/api/images/fetch', { ref } satisfies FetchRequest),
  };
}

export type ImagesApi = ReturnType<typeof createImagesApi>;

/** This server's own registry and GitHub settings; never proxied to a connected server. */
export function createLocalImagesApi() {
  const call = <T>(method: Verb, path: string, body?: unknown) => request<T>(method, path, body).then((r) => r.data);

  return {
    registries: () => call<RegistryView[]>('GET', '/api/settings/registries'),
    createRegistry: (req: RegistryRequest) => call<RegistryView>('POST', '/api/settings/registries', req),
    updateRegistry: (id: string, req: RegistryRequest) =>
      call<RegistryView>('PUT', `/api/settings/registries/${seg(id)}`, req),
    deleteRegistry: (id: string) => call<void>('DELETE', `/api/settings/registries/${seg(id)}`),
    testRegistry: (id: string) => call<RegistryTest>('POST', `/api/settings/registries/${seg(id)}/test`),

    githubSettings: () => call<GitHubSettings>('GET', '/api/settings/github'),
    setGitHubToken: (token: string) =>
      call<GitHubSettings>('PUT', '/api/settings/github', { token } satisfies GitHubTokenRequest),
    testGitHub: () => call<GitHubTest>('POST', '/api/settings/github/test'),

    imageDefaults: () => call<ImageDefaults>('GET', '/api/settings/images'),
    setImageDefaults: (defaults: ImageDefaults) => call<ImageDefaults>('PUT', '/api/settings/images', defaults),
  };
}
