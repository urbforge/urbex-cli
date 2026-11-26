# The release GitHub App

The release automation acts as a GitHub App of the `urbforge`
organization. Today that is `prepare-release`, which opens the release
pull requests (urbforge/urbex-cli#156). Next is the tag on merge
(urbforge/urbex-cli#157). It can't use the workflow's own
`GITHUB_TOKEN`:

- `GITHUB_TOKEN` may write only to its own repository, and
  `prepare-release` also pushes to `urbforge/urbex`;
- pull requests and tags made with `GITHUB_TOKEN` start no workflows, so
  CI wouldn't check the release pull requests and a tag wouldn't start
  the release.

A personal access token would act as one person and expire. The App
acts as itself (its commits and pull requests are `urbex-release[bot]`'s)
and has only the permissions below. Each run mints a token that lasts
an hour (`actions/create-github-app-token`).

## Set it up, once

An owner of `urbforge` does this:

1. **Create the App**: <https://github.com/organizations/urbforge/settings/apps/new>
   - *GitHub App name*: `urbex-release` (the commits show
     `urbex-release[bot]`).
   - *Homepage URL*: `https://github.com/urbforge/urbex-cli`.
   - *Webhook*: untick **Active**. The App receives no events.
   - *Repository permissions*: **Contents: Read and write**, **Pull
     requests: Read and write**. *Metadata: Read-only* is added by
     itself. Nothing else.
   - *Where can this GitHub App be installed?* **Only on this account**.
   - **Create GitHub App**.
2. On the App's page, note the **Client ID** (`Iv23...`). Under *Private
   keys*, **Generate a private key**: a `.pem` file is downloaded.
3. **Install it**: *Install App* → `urbforge` → **Only select
   repositories**: `urbex-cli` and `urbex` → **Install**.
4. **The secrets**, in `urbforge/urbex-cli` (the workflow runs there):

   ```sh
   gh secret set RELEASE_APP_CLIENT_ID -R urbforge/urbex-cli --body 'Iv23...'
   gh secret set RELEASE_APP_PRIVATE_KEY -R urbforge/urbex-cli < urbex-release.*.private-key.pem
   ```

   Or under *Settings* → *Secrets and variables* → *Actions* → *New
   repository secret*. Then delete the `.pem` file. If it is lost, generate
   a new one on the App's page and set the secret again.

The App doesn't bypass the branch rules: its pull requests need the same
review as anyone's.

## Prepare a release

*Actions* → **prepare-release** → *Run workflow*, with the version
(`0.3.0`), or:

```sh
gh workflow run prepare-release -R urbforge/urbex-cli -f version=0.3.0
```

In a minute there are two draft pull requests on `docs/release-v0.3.0`,
in `urbex-cli` and `urbex`. Each has the `CHANGELOG.md` draft of the
pull requests merged since the previous tag, and urbex's has the docs
moved to the new version. Their descriptions say what to edit before
review. It refuses a version that is tagged already, or whose branch
exists.

To see what it would do, without pushing anything, run it on clones of
both repositories, side by side, that you don't mind moving to a new
branch:

```sh
DOCS_DIR=../urbex tools/release/prepare.sh 0.3.0
```
