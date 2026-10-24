# Releasing kubexray

A release is a version tag. Pushing it runs [the release workflow](../.github/workflows/release.yml), which:

1. builds binaries for Linux, macOS and Windows (amd64 and arm64) with the UI embedded, writes an SPDX SBOM per archive, signs `checksums.txt` with cosign (keyless, through GitHub's OIDC identity), records build provenance, and creates a **draft** GitHub release with a changelog
2. builds the multi-arch image `ghcr.io/kubexray/kubexray:<version>` with an SBOM and provenance, signs it with cosign, and attests it
3. packages the Helm chart at the same version, pushes it to `oci://ghcr.io/kubexray/charts/kubexray`, signs it, and attests it

Images and the chart are published only if the binaries built. When the draft is published, [the Homebrew workflow](../.github/workflows/homebrew.yml) updates the cask in `kubexray/homebrew-tap` from the release's checksums, once that's set up (below). It waits for publishing so the cask never points at downloads that don't exist yet.

Nothing in the GitHub release is visible until a maintainer publishes the draft.

The pipeline is checked on every change to it (snapshot builds in [Release check](../.github/workflows/release-check.yml)), so a tag shouldn't be its first run.

## Cutting a release

```sh
git switch main && git pull
git tag -s v0.4.0 -m "kubexray v0.4.0"   # or -a without a signing key
git push origin v0.4.0
```

Then, on GitHub: **Actions → Release** finishes (about ten minutes); **Releases** has the draft. Read the changelog, add highlights at the top, and **Publish release**.

Images and the chart are pushed as GHCR packages. The first time, make them public: **github.com/orgs/kubexray/packages → kubexray → Package settings → Change visibility → Public**, and the same for **charts/kubexray**.

## Homebrew (one-time setup)

1. Create the public repository **kubexray/homebrew-tap** (empty, with a README).
2. Create a fine-grained token: **GitHub → Settings → Developer settings → Fine-grained tokens → Generate new token**. Resource owner `kubexray`, repository access *Only select repositories* → `homebrew-tap`, permission **Contents: Read and write**.
3. Add it to this repository: **Settings → Secrets and variables → Actions → New repository secret**, name `HOMEBREW_TAP_TOKEN`.

From the next release on, `brew install --cask kubexray/tap/kubexray` works. Without the secret, releases skip the cask and everything else still publishes.

## Verifying a release

```sh
cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/kubexray/kubexray/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt
sha256sum -c checksums.txt --ignore-missing

cosign verify ghcr.io/kubexray/kubexray:0.4.0 \
  --certificate-identity-regexp '^https://github.com/kubexray/kubexray/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

gh attestation verify kubexray_0.4.0_linux_amd64.tar.gz --repo kubexray/kubexray
```
