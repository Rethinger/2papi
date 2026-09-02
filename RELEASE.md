# Releasing 2papi

Releases are cut by pushing a `v*` tag. CI ([.github/workflows/ci.yml](.github/workflows/ci.yml))
runs goreleaser on that tag and publishes archives + `checksums.txt` to GitHub
Releases. Latest: [releases/latest](https://github.com/Rethinger/2papi/releases/latest).

## Runbook

```sh
# 1. Version metadata is stamped from the tag by goreleaser (ldflags),
#    so there is no version constant to bump. Update the changelog:
$EDITOR CHANGELOG.md          # add a section for the new version

# 2. Sanity-check the release config without publishing anything:
goreleaser check              # must report 0 deprecations
goreleaser release --snapshot --clean   # builds all targets locally into dist/

# 3. Land the changelog, then tag:
git push origin master
git tag vX.Y.Z
git push origin vX.Y.Z
```

The tag push triggers `go-test`, `control-plane-test`, `docker` and then
`release`. The release job is gated on `startsWith(github.ref, 'refs/tags/v')`
**and** on `on.push.tags: ['v*']` — without the trigger the job is unreachable and
silently never runs, which is why no release existed before v0.3.0.

## Verifying a release

```sh
gh release view vX.Y.Z --json isDraft,assets --jq '{draft:.isDraft,assets:[.assets[].name]}'
curl -s https://api.github.com/repos/Rethinger/2papi/releases/latest | grep tag_name
```

Then exercise the install path a user actually takes, in a container without Go:

```sh
docker run --rm alpine:3 sh -c 'apk add --no-cache curl tar >/dev/null &&
  INSTALL_DIR=/usr/local/bin sh -c "$(curl -fsSL https://raw.githubusercontent.com/Rethinger/2papi/master/install.sh)" &&
  2papi version'
```

`2papi version` must print the tagged version — `dev (commit none)` means the
binary was built without ldflags (i.e. from source, not from the release).

## Manual fallback

If Actions is unavailable:

```sh
goreleaser release --clean    # needs GITHUB_TOKEN with contents:write
# or, archives only, no publishing:
make cross                    # dist/2papi_{linux,darwin,windows}_{amd64,arm64}[.exe]
make build                    # bin/2papi (host)
```

## Distribution notes

- `install.sh` / `install.ps1` derive archive names by convention
  (`2papi_<os>_<arch>.tar.gz`, `.zip` on Windows) from `.goreleaser.yaml`'s
  `name_template`. Changing that template breaks both scripts — change them together.
- Windows/macOS binaries are unsigned, so SmartScreen and Gatekeeper will warn.
- Brew/scoop taps are configured but commented out in `.goreleaser.yaml`; they
  publish once `Rethinger/homebrew-tap` and `Rethinger/scoop-bucket` exist.
