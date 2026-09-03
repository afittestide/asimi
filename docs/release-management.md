# Release Management

This document describes the release process for Asimi CLI.

## Version Numbering

We follow [Semantic Versioning](https://semver.org/):

- **0.x.y** - Initial development; anything may change at any time
- **1.0.0+** - Public API is stable:
  - **MAJOR** (x.0.0) - Breaking changes
  - **MINOR** (2.x.0) - New features, backward compatible
  - **PATCH** (1.1.x) - Bug fixes, backward compatible

## Release Process

The instructions below use 0.2.1 as the version, please replace with the current version.

### 1. Prepare CHANGELOG.md

Review the git log since the last version and ensure all user notable changes are 
under the Unreleased section of the changle log.
Then, rename the section to 0.2.1 and add the date:

```markdown

## [0.2.1] - 2025-01-15

### Added
- New feature X

### Fixed
- Bug Y
```

### 2. Update Version in internal/utils/asimi_version.go

Edit line 17 in `internal/utils/asimi_version.go`:

```go
var AsimiVersion = "0.2.1" // Update this before each release
```

### 3. Update docs/roadmap.md


### 4. Commit the Release

```bash
git add CHANGELOG.md internal/utils/asimi_version.go README.md
git commit -m "chore: releasing 0.2.1"
```

### 5. Tag and Push

```bash
git tag -a v0.2.1 -m "Release 0.2.1"
git push origin v0.2.1
```

This triggers the GitHub Actions release workflow which:
- Builds binaries for all platforms
- Creates a GitHub release
- Updates the Homebrew formula

## Post-Release

### Verify the Installer

Test the one-liner installer:
```bash
curl -fsSL https://asimi.dev/installer | bash
# or
curl -fsSL https://raw.githubusercontent.com/afittestide/asimi/main/scripts/install.sh | bash
```

### Verify the Release

1. Check [GitHub Releases](https://github.com/afittestide/asimi/releases)
2. Verify binaries are attached
3. Test Homebrew installation:
   ```bash
   brew upgrade asimi
   asimi --version
   ```

### Announce (Optional)

- GitHub Discussions
- Social media
- Community channels

## Quick Reference

```bash
# Full release flow
vim CHANGELOG.md                                                   # Polish changelog
sed -i 's/var AsimiVersion = .*/var AsimiVersion = "0.2.1"/' internal/utils/asimi_version.go  # Update version
vim README.md                                                      # Update roadmap
git add CHANGELOG.md internal/utils/asimi_version.go README.md && git commit -m "chore: releasing 0.2.1"
git tag -a v0.2.1 -m "Release 0.2.1"
git push origin main --tags
```


## One-Liner Installer

The one-liner installer (`scripts/install.sh`) is available for direct installation of Asimi.

### Hosting

The installer should be accessible at `https://asimi.dev/installer`. There are several options:

1. **GitHub Raw URL (Fallback)**: The script is always available at:
   ```
   https://raw.githubusercontent.com/afittestide/asimi/main/scripts/install.sh
   ```

2. **Domain Redirect**: Configure `asimi.dev/installer` to redirect to the GitHub raw URL.

3. **Static Hosting**: Host the script on a CDN or static site (Netlify, Vercel, GitHub Pages).

### Updating the Installer

When making changes to the installer:
1. Edit `scripts/install.sh`
2. Test locally: `bash scripts/install.sh`
3. Commit and push to main
4. The GitHub raw URL updates automatically

### Installer Options

Users can customize installation via environment variables:
- `ASIMI_INSTALL_DIR` - Custom installation directory
- `ASIMI_VERSION` - Install a specific version (e.g., `v0.2.0`)
- `ASIMI_NO_MODIFY_PATH` - Skip automatic PATH modification

Example:
```bash
ASIMI_VERSION=v0.2.0 ASIMI_INSTALL_DIR=~/bin curl -fsSL https://asimi.dev/installer | bash
```

## Automated Release

The `release-version` ritual (defined in `.agents/rituals.yaml`) automates this
process end-to-end. The Chancellor enacts it via `enact_ritual` with a target
version (e.g. `0.9.0`). The ritual runs through changelog preparation, version
bump, roadmap update, verification, sage review, commit-and-tag, and finally
asks the Ruler to confirm the push via zhengming.

## See Also

- [RELEASE_CHECKLIST.md](../RELEASE_CHECKLIST.md) - Detailed Homebrew release checklist
- [CHANGELOG.md](../CHANGELOG.md) - Version history
