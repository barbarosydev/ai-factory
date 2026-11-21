---
name: upgrade-version
description: Bump pinned versions in mise.toml, go.mod, or GitHub Actions workflow SHAs.
---

# Upgrade a pinned version

1. Update CLI pins in `mise.toml`, run `mise lock --platform macos-arm64,linux-x64,linux-arm64`, and run `mise install`.
2. Update Go module versions with `go get <pkg>@<version> && go mod tidy`.
3. Update GitHub Actions in `.github/workflows/*.yml` to full 40-hex commit SHAs with trailing version comments.
4. Run `task ci`.
