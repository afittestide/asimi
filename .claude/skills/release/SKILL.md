---
name: release
description: Run the full release process for asimi - update changelog, bump version, update roadmap, commit, and tag. Use when the user says "release", "cut a release", "prepare release", or similar.
user-invocable: true
argument-hint: [version]
---

You are releasing version `$ARGUMENTS` of asimi.

If no version argument was provided, read the current version from line 34 of `main.go` (the `var version` line), determine the next appropriate version by reviewing the CHANGELOG.md unreleased section, and ask the user to confirm before proceeding.

Follow these steps in order:

## 1. Prepare CHANGELOG.md

- Read `CHANGELOG.md`
- Read git log since the last tagged release: `git log $(git describe --tags --abbrev=0)..HEAD --oneline`
- Ensure all user-notable changes are listed under an `## Unreleased` section
- Rename `## Unreleased` to `## [$ARGUMENTS] - YYYY-MM-DD` using today's date
- Organize entries under: `### Added`, `### Changed`, `### Fixed`, `### Removed` (only include sections that have entries)

## 2. Update version in main.go

Edit line 34 in `main.go` to set:
```go
var version = "$ARGUMENTS"
```

## 3. Update README.md Roadmap

- Read `README.md` and find the Roadmap section
- Check which issues referenced in the roadmap are now closed: `gh issue list --state closed`
- Remove closed issues from the roadmap table
- Check for new open issues that should be on the roadmap: `gh issue list --state open`
- Add relevant new open issues to the roadmap table using the format:
  ```
  | [#XX - Feature Name](https://github.com/afittestide/asimi/issues/XX) | Brief description |
  ```

## 4. Show the user a summary of all changes and ask for approval before committing

Present a diff summary and wait for explicit user confirmation. **DON'T commit or tag without approval.**

## 5. Commit the release (only after user approval)

```bash
git add CHANGELOG.md main.go README.md
git commit -m "chore: releasing $ARGUMENTS"
```

## 6. Tag (only after user approval)

```bash
git tag -a v$ARGUMENTS -m "Release $ARGUMENTS"
```

Tell the user the tag is created locally. Remind them to push with:
```bash
git push origin main --tags
```

Do NOT push automatically — let the user decide when to push.

## Important rules

- Use present progressive tense in changelog entries ("adding feature" not "added feature")
- Follow SemVer: major.minor.patch
- Do NOT push to remote — only commit and tag locally
- Do NOT merge — ask user for approval at every significant step
