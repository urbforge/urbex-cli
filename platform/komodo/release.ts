// Komodo Action "urbex-release" - managed by urbex bootstrap. See
// docs/decisions/0020-trunk-releases-gitops-environments.md and
// docs/decisions/0021-staging-follows-main.md in urbforge/urbex.
//
// Gitea calls it on every push to a project repo:
//
//   - a push to main builds every service of the project at that commit
//     (image tag: the short commit hash), then sets every environment
//     that follows main - in practice staging - to it, with a commit to
//     the GitOps repo; Komodo deploys from there;
//   - a release tag (vX.Y.Z) gives the tagged commit's images the tag
//     X.Y.Z, without rebuilding them if main already built that commit;
//   - anything else is ignored.
//
// ARGS.WEBHOOK_BRANCH is the pushed ref without "refs/heads/" (a tag
// arrives as "refs/tags/v1.4.2"); ARGS.WEBHOOK_BODY is Gitea's payload.
// The [[URBEX_...]] values are Komodo variables set by urbex bootstrap.

const GITEA = "[[URBEX_GITEA_URL]]";
const GITEA_USER = "[[URBEX_GITEA_USER]]";
const GITEA_TOKEN = "[[URBEX_GITEA_TOKEN]]";
const ORG = "[[URBEX_ORG]]";
const GITOPS_REPO = `${ORG}/gitops`;
const GITOPS_PROCEDURE = "urbex-gitops";
const TRUNK = "main";

const ref: string = ARGS.WEBHOOK_BRANCH ?? "";
const body = ARGS.WEBHOOK_BODY ?? {};
const repo: string = body.repository?.full_name ?? "";
// For a branch, the pushed commit. For a tag, the ref's new value: the
// commit for a lightweight tag, but the tag object for an annotated one
// pushed with git - the release path resolves it to the commit.
let commit: string = body.after ?? "";
const project = repo.split("/")[1] ?? "";
const release = ref.match(/^refs\/tags\/v(\d+)\.(\d+)\.(\d+)$/);

const giteaAuth = "Basic " + btoa(`${GITEA_USER}:${GITEA_TOKEN}`);

async function gitea(method: string, path: string, payload?: unknown): Promise<any> {
  const res = await fetch(`${GITEA}/api/v1${path}`, {
    method,
    headers: { Authorization: giteaAuth, "Content-Type": "application/json" },
    body: payload === undefined ? undefined : JSON.stringify(payload),
  });
  if (res.status === 404) return undefined;
  if (!res.ok) throw new Error(`gitea ${method} ${path}: ${res.status} ${await res.text()}`);
  return res.status === 204 ? undefined : await res.json();
}

// --- registry: tags are manifests; retagging copies one ---------------

const MANIFEST_TYPES = [
  "application/vnd.oci.image.index.v1+json",
  "application/vnd.docker.distribution.manifest.list.v2+json",
  "application/vnd.oci.image.manifest.v1+json",
  "application/vnd.docker.distribution.manifest.v2+json",
].join(", ");

async function registryToken(image: string): Promise<string> {
  const scope = `repository:${ORG}/${image}:pull,push`;
  const res = await fetch(`${GITEA}/v2/token?service=container_registry&scope=${scope}`, {
    headers: { Authorization: giteaAuth },
  });
  if (!res.ok) throw new Error(`registry token for ${image}: ${res.status}`);
  return (await res.json()).token;
}

async function getManifest(image: string, tag: string, token: string) {
  const res = await fetch(`${GITEA}/v2/${ORG}/${image}/manifests/${tag}`, {
    headers: { Authorization: `Bearer ${token}`, Accept: MANIFEST_TYPES },
  });
  if (res.status === 404) return undefined;
  if (!res.ok) throw new Error(`manifest ${image}:${tag}: ${res.status}`);
  return { type: res.headers.get("Content-Type") ?? "", body: new Uint8Array(await res.arrayBuffer()) };
}

// The image main built for a commit, under whichever abbreviation of the
// hash git used (7 characters, unless that was ambiguous).
async function commitImage(image: string, token: string) {
  for (let length = 7; length <= 12; length++) {
    const tag = commit.slice(0, length);
    const manifest = await getManifest(image, tag, token);
    if (manifest) return { tag, ...manifest };
  }
  return undefined;
}

async function retag(image: string, manifest: { type: string; body: Uint8Array }, tag: string, token: string) {
  const res = await fetch(`${GITEA}/v2/${ORG}/${image}/manifests/${tag}`, {
    method: "PUT",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": manifest.type },
    body: manifest.body,
  });
  if (!res.ok) throw new Error(`tagging ${image}:${tag}: ${res.status} ${await res.text()}`);
}

// --- builds ----------------------------------------------------------

async function projectBuilds(): Promise<any[]> {
  const builds = (await komodo.read("ListFullBuilds", {})).filter((b: any) => b.config.repo === repo);
  if (builds.length === 0) {
    throw new Error(`no Komodo Build uses repo ${repo} - run 'urbex apply' for the project first`);
  }
  return builds;
}

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

// Builds the commit for every given Build; returns the image tag (short
// commit hash) of each, by Build name. Several runs can build the same
// project at once - the webhook of a push and 'urbex apply', or pushes in
// quick succession - so each waits for a busy Build, and a commit whose
// image already exists isn't built again.
async function buildCommit(builds: any[]): Promise<Record<string, string>> {
  const tags = await Promise.all(builds.map((build: any) => buildOne(build.name)));
  const result: Record<string, string> = {};
  builds.forEach((build: any, i: number) => (result[build.name] = tags[i]));
  return result;
}

async function buildOne(name: string): Promise<string> {
  const deadline = Date.now() + 30 * 60 * 1000;
  while (Date.now() < deadline) {
    const existing = await commitImage(name, await registryToken(name));
    if (existing) return existing.tag;
    const state = await komodo.read("GetBuildActionState", { build: name });
    if (state?.building) {
      await sleep(5000);
      continue;
    }
    await komodo.write("UpdateBuild", { id: name, config: { commit } });
    const update = await komodo.execute_and_poll("RunBuild", { build: name });
    if (update.success) return update.commit_hash || commit.slice(0, 7);
    const busy = (update.logs ?? []).some((log: any) => /busy/i.test(`${log.stderr} ${log.stdout}`));
    if (!busy) throw new Error(`build of ${commit.slice(0, 7)} failed for ${name}`);
    await sleep(5000);
  }
  throw new Error(`build of ${commit.slice(0, 7)} for ${name}: gave up waiting for the Build to be free`);
}

// --- the GitOps repo: environments that follow main --------------------

function b64decode(s: string): string {
  return atob(s.replace(/\n/g, ""));
}

// Sets every environment whose version.env says TRACK=main to the given
// image tags, in one commit to the GitOps repo, and makes sure Komodo
// deploys it.
async function followMain(tags: Record<string, string>) {
  // Runs can finish out of order: only main's head moves environments,
  // or a slow build of an older push would undo a newer one.
  const head = await gitea("GET", `/repos/${repo}/branches/${TRUNK}`);
  if (head?.commit?.id && head.commit.id !== commit) {
    console.log(`main has moved on to ${head.commit.id.slice(0, 7)}: its run deploys it, not ${commit.slice(0, 7)}.`);
    return;
  }
  for (let attempt = 1; ; attempt++) {
    try {
      return await setFollowers(tags);
    } catch (err) {
      // Another run committed to the GitOps repo since we read it.
      if (attempt === 5) throw err;
      await sleep(2000);
    }
  }
}

async function setFollowers(tags: Record<string, string>) {
  const envs = ((await gitea("GET", `/repos/${GITOPS_REPO}/contents/environments`)) ?? [])
    .filter((e: any) => e.type === "dir")
    .map((e: any) => e.name);
  const files: any[] = [];
  const moved: string[] = [];
  for (const env of envs) {
    for (const [buildName, tag] of Object.entries(tags)) {
      const service = buildName.slice(project.length + 1);
      const path = `environments/${env}/${project}/${service}/version.env`;
      const file = await gitea("GET", `/repos/${GITOPS_REPO}/contents/${path}`);
      if (!file) continue;
      const content = b64decode(file.content);
      if (!/^TRACK=main$/m.test(content)) continue;
      const updated = /^VERSION=.*$/m.test(content)
        ? content.replace(/^VERSION=.*$/m, `VERSION=${tag}`)
        : content.trimEnd() + `\nVERSION=${tag}\n`;
      if (updated === content) continue;
      files.push({ operation: "update", path, sha: file.sha, content: btoa(updated) });
      if (!moved.includes(env)) moved.push(env);
    }
  }
  if (files.length === 0) {
    console.log("No environment follows main for this project: nothing to deploy.");
    return;
  }
  await gitea("POST", `/repos/${GITOPS_REPO}/contents`, {
    branch: TRUNK,
    message: `urbex: ${project} main@${commit.slice(0, 7)} to ${moved.join(", ")}`,
    files,
  });
  console.log(`Set ${moved.join(", ")} to ${project} main@${commit.slice(0, 7)} in ${GITOPS_REPO}.`);
  // The GitOps repo's webhook deploys this commit - unless Komodo looked
  // at the repo in the last few seconds and still has it cached. Make
  // sure, once that window has passed.
  await sleep(6000);
  await komodo.execute("RunProcedure", { procedure: GITOPS_PROCEDURE });
}

// --- main ------------------------------------------------------------

if (!repo || !/^[0-9a-f]{40}$/.test(commit) || /^0+$/.test(commit)) {
  console.log(`'${ref}': no repository or commit (a deletion?) - nothing to do.`);
} else if (ref === TRUNK) {
  const builds = await projectBuilds();
  console.log(`Building ${repo} main@${commit.slice(0, 7)}: ${builds.map((b: any) => b.name).join(", ")}`);
  const tags = await buildCommit(builds);
  await followMain(tags);
} else if (release) {
  const version = `${release[1]}.${release[2]}.${release[3]}`;
  const tag = await gitea("GET", `/repos/${repo}/tags/${encodeURIComponent(ref.slice("refs/tags/".length))}`);
  if (!tag?.commit?.sha) throw new Error(`${repo}: tag ${ref} not found`);
  commit = tag.commit.sha;
  const builds = await projectBuilds();
  console.log(`Releasing ${repo} ${version} (${commit.slice(0, 7)}): ${builds.map((b: any) => b.name).join(", ")}`);
  // Main normally built this commit already: reuse its images. Build
  // the ones it didn't (a tag on a commit that never was main's head).
  const missing: any[] = [];
  for (const build of builds) {
    const token = await registryToken(build.name);
    if (!(await commitImage(build.name, token))) missing.push(build);
  }
  if (missing.length > 0) {
    console.log(`Building ${missing.map((b: any) => b.name).join(", ")}: main never built ${commit.slice(0, 7)}.`);
    await buildCommit(missing);
  }
  for (const build of builds) {
    const token = await registryToken(build.name);
    const manifest = await commitImage(build.name, token);
    if (!manifest) throw new Error(`release ${version}: no image of ${build.name} for ${commit.slice(0, 7)}`);
    await retag(build.name, manifest, version, token);
  }
  console.log(`Release ${version} tagged: ${builds.map((b: any) => `${b.name}:${version}`).join(", ")}.`);
} else {
  console.log(`'${ref}' is neither ${TRUNK} nor a release tag (vX.Y.Z): nothing to do.`);
}
