# Image library (build, version, push, share) — design

Date: 2026-10-23
Status: draft, awaiting approval

## Intent

Today a service can build from its own Dockerfile inside its stack's folder.
That Dockerfile belongs to one service, has no history, and its image has no
versions, cannot be reused by other stacks, cannot be pushed to a registry,
and cannot be used on a connected server.

Stackstead gets a top-level **Images** library: named images built from
Dockerfiles into tagged versions. Any service on any connected server can
pin a version. Versions can be pushed to Docker Hub and Amazon ECR, and old
versions are cleaned up by policy.

## Goals

- A library of images, each with one of two sources:
  - **Uploaded**: a Dockerfile written in Stackstead plus uploaded files
    (the build context). Every saved edit is kept as a revision that can be
    viewed and rolled back to.
  - **GitHub**: a repository, a branch, and a path inside the repo. The
    Dockerfile is read-only in Stackstead. Only build history is kept.
- Builds produce **versions**. Each version has a tag, the source revision
  or commit, the build args used, the architecture, the time, the result,
  and the full build log.
- Stackstead **suggests the next tag** and the user can change it.
- **Registry connections** in Settings: Docker Hub, and ECR with three
  credential modes. Each image has **push targets**, with **auto-push** for
  uploaded images.
- A service in any stack can **pick an image and version** from the
  library, stays pinned to it, and shows **"new version available"** when a
  newer one exists.
- The library is **shared across all connected servers (peers)**. A server
  that needs another server's version fetches it over the peer link.
- **Retention policies** per image, with a default in Settings.
- **Build arguments**, with saved defaults that can be overridden per
  build.

## Non-goals

- Multi-architecture builds, buildx, or QEMU. A build uses only the
  architecture of the server that runs it. (The user first asked for both
  architectures, then deliberately chose this to keep it simple.)
- Automatic builds or pushes for GitHub images: no webhooks, no polling,
  no auto-push.
- Editing GitHub-sourced Dockerfiles, or committing back to the repo.
- Build secrets (`--secret`), target stages, cache configuration, or other
  build flags beyond build args.
- Deleting tags from registries. Retention and manual deletion act locally
  only.
- Pulling library images *from* registries onto peers. Peers get images
  over the peer link.
- Registries other than Docker Hub and ECR (for example GHCR or a
  self-hosted registry). The registry model should not make these hard to
  add later.
- Auto-updating services to new versions.

## Anti-goals (would make this a failure even if it works)

- Registry or GitHub credentials leaking into logs, build output, API
  responses, the action log, or the browser. (Follow the `peers.Secret`
  redaction pattern.)
- A retention run or a manual delete that removes a version a service on
  any peer still uses.
- Breaking stacks so they no longer work with plain `docker compose`. The
  compose file must reference a normal image reference (`name:tag`) that
  Docker can resolve locally.
- An upload that writes outside the image's own folder (path traversal,
  symlinks), or a GitHub fetch that does the same.
- Silently using a version built for the wrong architecture.

## Decisions made with the user

1. **Two source types**: uploaded files, or a path in a GitHub repo.
2. **Versioning means both** revision history of the Dockerfile and files
   (uploaded images only) and tagged builds.
3. **GitHub images keep build history only**, with the commit recorded per
   version.
4. **Tag**: Stackstead suggests one and the user can edit it before
   building.
5. **Services stay pinned** and show a "new version available" flag. The
   user changes the version by hand.
6. **Auto-push**: when a push target has auto-push on and its registry
   connection is configured, a successful build of an *uploaded* image
   pushes automatically. GitHub images are never built or pushed
   automatically. Any version can be pushed by hand.
7. **ECR credentials**: support access key and secret, the host's IAM role,
   and a named AWS profile.
8. **Shared to all peers.** When server B uses a version built on server A,
   B fetches the image from A, loads it into its own Docker, and runs it
   from there.
9. **Architecture is native**: Linux amd64 builds amd64, Linux arm64 and
   macOS build arm64.
10. **Retention by policy**: no policy means keep forever. A policy can keep
    the last N versions and/or versions younger than N days.
11. **Retention cleans up locally only**, never in registries.
12. **Build arguments are supported.**

## Calls made by Claude (user may override)

- **GitHub auth is a personal access token** (fine-grained or classic)
  stored in Settings. A GitHub App or OAuth is out of scope.
- **Tag suggestion**: if the latest tag is semver-like (`1.4.0`, `v1.4`),
  bump the last numeric part. Otherwise suggest `1` for the first build and
  `<n+1>` when the latest tag is an integer. Otherwise leave the field empty
  with the latest tag as a hint. Tags must be valid Docker tags and unique
  per image.
- **Image ownership**: each image is owned by the server it was created on.
  Only that server stores its source and revisions, runs its builds and
  pushes, and applies its retention policy. Peers see it read-only and can
  fetch its versions. Other servers' images are labelled with the owning
  server's name.
- **When a peer fetches**: when the user picks a version for a service on
  that peer, and before a stack starts if the image is missing locally.
  A version whose architecture differs from the peer's is shown but can't
  be picked, with a note such as "built for arm64; this server is amd64".
- **Multiple push targets per image** are allowed.
- **The retention default in Settings** applies to newly created images.
  Changing it does not rewrite existing images' policies.
- **Retention never touches Dockerfile revisions** of uploaded images.
  They are small and are what makes rollback possible.
- **"In use"** means referenced by any service's `image:` in any stack on
  any reachable peer. If a peer is unreachable, retention skips deleting
  that image's versions on that run, so it never guesses. Manual delete of
  an in-use version is refused, with the list of using services.
- **Build args**: per-image saved defaults (key/value). The Build dialog
  prefills them, and changes apply to that build only (with an option to
  save them as the new defaults). The values used are stored with the
  version. Build args are not secrets and are shown in plain text. The UI
  says so.
- **Builds**: output streams live to the browser. The log is kept with the
  version. One build at a time per image, and a second request is refused
  with a clear message. A failed build is recorded as a failed version
  attempt but doesn't take the tag, so the tag can be reused.
- **Local image reference**:
  `stackstead.invalid/<owner-id>/<image-slug>:<tag>`. `.invalid` is a
  reserved TLD (RFC 2606) that never resolves, so a `docker pull` or
  `compose pull` can never fetch a stranger's image (`stackstead/...` is a
  real Docker Hub namespace). `<owner-id>` is a short, stable identifier of
  the owning server, derived from its instance ID rather than its renamable
  display name. This keeps references unique across peers even when two
  servers have an image with the same slug. The compose file references
  this name, so the stack keeps working with plain `docker compose` on that
  host once the image is present. The same reference is used on every peer
  after fetching. Stackstead's own stack "pull" action skips library
  services, and fetches them over the peer link instead when they are
  missing.
- **Where image actions are allowed**: images are managed on their owning
  server. As with stacks, the web app can manage a connected server's
  images through the existing forwarding route, because that acts *on* the
  owner. The peer-to-peer surface (`/api/peer/...`) for images is read-only:
  listing, version metadata, used-by queries, and image transfer. A peer
  server cannot act on another server's images *on its own behalf*: the
  only write path is the operator's forwarded `/api/images/...` calls,
  exactly as with stacks. `/api/settings/...` (registry and GitHub
  credentials, defaults) stays peer-denied, so credentials are only
  managed on the server itself.
  The merged library is assembled server-side from those read-only calls.
- **Sharing reaches directly paired servers only**: a server sees and can
  fetch images only from servers it is paired with directly. "In use" for
  retention means in use on the owner or any of its direct peers. A
  non-mesh topology (A–B, B–C) does not let C use A's images.
- **GitHub path** is the build-context directory. The Dockerfile defaults
  to `Dockerfile` at its root, with an optional Dockerfile path relative to
  the context. Uploaded images get the same optional field.

## Shape of the feature

### Images area (web)

- List: name, source type, owning server, latest version, last build
  result, push targets.
- Image page tabs:
  - **Source**: for uploaded images, a Dockerfile editor and a file tree
    with upload (files and folders), rename, and delete. Saving creates a
    revision. For GitHub images, repo, branch, and path settings and a
    read-only view of the Dockerfile at the branch head.
  - **Revisions** (uploaded only): a list with time, a diff against the
    previous revision, and "restore", which creates a new revision equal to
    the old one.
  - **Versions**: tag, source revision or commit, architecture, build args,
    time, status, push status per target, the services using it, the build
    log, a "push" action per target, and delete.
  - **Settings**: build arg defaults, push targets (registry connection,
    repository, auto-push), and retention policy.
- Build dialog: suggested tag (editable), build args (prefilled). GitHub
  images also get a branch-head-or-commit selector.

### Settings (web)

- **GitHub connection**: token, shown masked, with "test connection".
- **Registry connections** (several allowed, each named):
  - Docker Hub: username and access token.
  - ECR: region, plus one of access key and secret / IAM role / AWS
    profile name. The registry host comes from the account and region.
- **Default retention policy** for new images.

### Services in stacks

- The service form's image field gains "from library": pick an image
  (from any peer) and a version. This writes `image:
  stackstead.invalid/<owner-id>/<slug>:<tag>` into the compose file, using the existing
  comment-preserving editing.
- Stackstead recognizes library references by that namespace and shows the
  image name, the version, and a "new version available" badge on the
  service and stack views when a newer successful version exists.

### Peers

- Library listing and version metadata are exposed over the existing
  peer protocol (`/api/peer/...`), so any server can show the merged
  library.
- Image transfer: the target server asks the owner for a version and the
  owner streams `docker save` (Engine API `GET /images/{name}/get`) for
  that image. The target loads it (`POST /images/load`) and checks that the
  resulting image ID matches the version's recorded ID.
- Used-by queries over the peer link support retention and delete checks.

### CLI

At minimum: `stackstead images list`, `stackstead images build <image>
[--tag] [--build-arg K=V]...` (streams the log), and `stackstead images
push <image> <tag> [--target]`. Following the existing CLI conventions
(`docs/cli.md`).

## Constraints

- **Small dependency footprint.** `go.mod` currently holds only `yaml`,
  `golang.org/x/*`, and `sqlite`. Docker is reached through the hand-rolled
  `internal/engine` client over the unix socket. Builds (`POST /build` with
  a tar context), pushes (`POST /images/{name}/push` with `X-Registry-Auth`),
  save and load all fit that client.
- **ECR auth** needs SigV4-signed `GetAuthorizationToken` calls and the
  three credential sources (static keys, IMDSv2 instance role, shared
  config and credentials files for a profile). Whether to hand-roll this or
  add `aws-sdk-go-v2` is open (see "Open for the builder").
- **Storage**: image source trees and revisions live under `DataDir`
  (default `/var/lib/stackstead/images/<slug>/...`). Metadata goes in the
  SQLite store as new append-only migrations (`internal/store`).
- **Secrets at rest** live in SQLite like the existing peer credentials,
  with the same redaction types. The data directory stays `0700`.
- **Upload limits**: enforce a per-file and a per-context size cap
  (configurable), reject symlinks and paths that escape, and honour
  `.dockerignore` when building the context tar.
- **Every build, push, fetch, retention removal, and credential change**
  goes into the existing action log, without secret values.
- **Single admin account**: there are no per-user permissions.
- Existing per-service Dockerfile builds keep working unchanged.

- **AWS profiles mean static keys only** (`aws_access_key_id` /
  `aws_secret_access_key` / optional `aws_session_token` in
  `~/.aws/credentials` or `~/.aws/config`). SSO, `role_arn` and
  `credential_process` profiles are out of scope. This lets ECR auth be
  hand-rolled (SigV4 + IMDSv2 + INI parsing) with no AWS SDK dependency.
  (Recommended by Claude; the user approved the doc with it.)

## Open for the builder
- How revisions are stored (full snapshots vs. content-addressed files)
  and how a revision is turned into a build context tar.
- The exact peer endpoints and the streaming and timeout behavior of image
  transfer for large images.
- When retention runs (after each build plus a periodic sweep is the
  expected shape).
- UI layout details within the existing design system.
