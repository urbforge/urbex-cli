// Komodo Action "urbex-release" - managed by urbex bootstrap. See
// docs/decisions/0020-trunk-releases-gitops-environments.md in
// urbforge/urbex.
//
// Gitea calls it on every push to a project repo. When the push is a
// release tag (vX.Y.Z), it points every Build of that repo at the tagged
// commit, sets its version to X.Y.Z, and runs it - producing the
// <project>-<service>:X.Y.Z images that environments then deploy. Any
// other push (a commit on main, another tag) is ignored.
//
// ARGS.WEBHOOK_BRANCH is the pushed ref without "refs/heads/", so a tag
// arrives as "refs/tags/v1.4.2"; ARGS.WEBHOOK_BODY is Gitea's payload.

const ref: string = ARGS.WEBHOOK_BRANCH ?? "";
const release = ref.match(/^refs\/tags\/v(\d+)\.(\d+)\.(\d+)$/);

if (!release) {
  console.log(`'${ref}' is not a release tag (vX.Y.Z): nothing to build.`);
} else {
  const body = ARGS.WEBHOOK_BODY ?? {};
  const repo: string = body.repository?.full_name ?? "";
  const commit: string = body.after ?? "";
  const version = {
    major: Number(release[1]),
    minor: Number(release[2]),
    patch: Number(release[3]),
  };
  const label = `${version.major}.${version.minor}.${version.patch}`;
  if (!repo || !/^[0-9a-f]{40}$/.test(commit)) {
    throw new Error(`release ${label}: the webhook payload has no repository or commit`);
  }

  const builds = (await komodo.read("ListFullBuilds", {})).filter(
    (build: any) => build.config.repo === repo,
  );
  if (builds.length === 0) {
    throw new Error(`release ${label}: no Komodo Build uses repo ${repo} - run 'urbex apply' for the project first`);
  }

  console.log(`Releasing ${repo} ${label} (${commit.slice(0, 7)}): ${builds.map((b: any) => b.name).join(", ")}`);
  for (const build of builds) {
    await komodo.write("UpdateBuild", { id: build.name, config: { version, commit } });
  }
  const updates = await Promise.all(
    builds.map((build: any) => komodo.execute_and_poll("RunBuild", { build: build.name })),
  );
  const failed = builds.filter((_: any, i: number) => !(updates[i] as any).success).map((b: any) => b.name);
  if (failed.length > 0) {
    throw new Error(`release ${label}: build failed for ${failed.join(", ")}`);
  }
  console.log(`Release ${label} built.`);
}
