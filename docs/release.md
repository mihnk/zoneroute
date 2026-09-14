# Cutting a release

For maintainers. Releases are tag-driven: pushing a SemVer tag runs
`.github/workflows/release.yml`, which validates, runs the functional suite,
builds and pushes the image, and publishes a GitHub Release whose install
manifest pins the published image digest.

## What a release produces

| Artifact | Reference |
| --- | --- |
| Container image | `ghcr.io/mihnk/zoneroute:vX.Y.Z` and `ghcr.io/mihnk/zoneroute@sha256:…` (linux/amd64, linux/arm64) |
| Convenience tag | `ghcr.io/mihnk/zoneroute:latest`, moved only for stable releases |
| Release asset | `install.yaml` — the `config/default` bundle with the image pinned **by digest** |
| Release asset | `checksums.txt` — SHA256 of `install.yaml` |
| Release notes | the version's section of `CHANGELOG.md`, plus the install URL and digest |

The repository's committed `install.yaml` is the **development** manifest and
keeps the `:dev` image convention. A release never rewrites it.

Version tags are immutable. `v0.1`, `v0` and `sha-…` tags are deliberately
not published: the release manifest and the documentation name a digest or an
exact version, so a moving minor tag would only invite someone to depend on
one.

## Readiness checklist

Before tagging:

- [ ] every issue in the milestone is closed and nothing is left open that
      blocks the version
- [ ] `verify` is green on the commit to be tagged
- [ ] `e2e` is green on the commit to be tagged
- [ ] `make verify-generate` is clean
- [ ] `make test` passes
- [ ] `make verify-crd` and `make verify-install` pass on a machine with
      Docker
- [ ] `CHANGELOG.md` has a section for the version, and its limitations are
      still true
- [ ] the documentation matches what the release will publish — install URL,
      image reference, compatibility table
- [ ] `LICENSE` is present; `SECURITY.md`, `CONTRIBUTING.md` and the code of
      conduct are available from the organization's `.github` repository
- [ ] a dry run has passed: **Actions → release → Run workflow** with the
      version, which validates, runs e2e and builds both platforms without
      pushing anything

Locally, without pushing anything:

```sh
VERSION=v0.2.0 make release-check
```

This checks the version syntax, renders the manifest, the checksums and the
release notes into `hack/.release/<version>/`, and prints the image line. The
dry run pins the tag, because no digest exists yet; a real release pins the
digest.

## Cutting the tag

```sh
git switch main && git pull
VERSION=v0.2.0 make release-check
git tag -a v0.2.0 -m 'ZoneRoute v0.2.0'
git push origin v0.2.0
```

Then watch the workflow. The jobs run in this order, and each one is a gate
for the next:

```
validate  →  e2e  →  build  →  release  →  latest
```

- **validate** — version syntax, `verify-generate`, `vet`, `test -race`.
- **e2e** — the full kind suite against real CoreDNS, on the tagged commit.
  A release tag always reruns it rather than trusting an earlier run. The
  suite scales the test cluster's CoreDNS to one replica and shortens its
  reload interval (`ZONEROUTE_TEST_TUNING=1` in `hack/wire-coredns.sh`),
  which only changes how fast the test cluster converges — never what is
  asserted, and never anything a real cluster should copy.
- **build** — refuses to overwrite an already published version, builds both
  platforms, pushes one multi-platform index, attaches an SBOM and
  provenance, and attests the pushed digest.
- **release** — renders the digest-pinned manifest, creates a **draft**
  release, uploads the assets, checks that both are present, and only then
  publishes it.
- **latest** — moves `:latest`, for stable releases only, after the release
  is public.

## Prereleases

`v0.1.0-rc.1` and similar are valid. The GitHub Release is marked as a
prerelease and is not made the repository's "latest" release, and the
`:latest` image tag is not moved.

## Recovery from a partial failure

The version tag and the published image are immutable. Recovery means
rerunning the failed jobs, never moving the tag.

| What failed | What the public sees | What to do |
| --- | --- | --- |
| `validate` or `e2e` | nothing — no image, no release | fix the problem on `main`, then release the next version. The tag has already been used; do not move it |
| `build`, before the push | nothing | rerun the job. Registry blobs may have been uploaded by the failed build, but no version tag points at them |
| `build`, after the push | the image exists; no release | rerun the job. It finds the published tag, reuses that digest, and skips the build |
| `release`, while uploading | the image exists; the release is still a **draft**, invisible to anyone without write access | rerun the job. It updates the same draft, re-uploads the assets, and publishes |
| `release`, at publication | image plus draft | rerun the job |
| `latest` | the release is complete and correct; `:latest` still points at the previous version | rerun the job, or move the tag by hand with `docker buildx imagetools create` |

Two consequences worth stating plainly:

- A failure never publishes a release that names an image that does not
  exist, because the image is pushed before the release is created and the
  release is a draft until its assets are verified.
- Buildx pushes one multi-platform index, so a published version tag is never
  a single architecture. It does not mean nothing at all reached the registry
  during a failed build — unreferenced blobs and manifests may have — only
  that no version tag names them.

If a release must be abandoned before it is public, delete the draft
(`gh release delete vX.Y.Z`) and release the next version. Do not delete a
published release, and do not delete a published image: someone may already
have pulled it by digest.

## Manual steps outside the repository

1. **GHCR package visibility.** After the first publish, check that the
   package is readable without authentication:

   ```sh
   docker pull ghcr.io/mihnk/zoneroute:v0.2.0   # from a logged-out client
   ```

   If it is not public, set it in the package settings
   (`github.com/orgs/mihnk/packages/container/zoneroute/settings` → *Change
   visibility* → *Public*), and check that the repository has access under
   *Manage Actions access*. Depending on the organization's defaults this may
   already be the case.

2. **Branch protection.** Once the e2e concurrency fix has produced several
   stable green runs on pull requests and on `main`, add the check named
   `e2e` to the ruleset for `main`, alongside `verify`. This is a GitHub
   setting, not a repository file.
