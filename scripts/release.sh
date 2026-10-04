#!/usr/bin/env bash
# Tag a release.
#
#   ./scripts/release.sh 1.1.0
#   ./scripts/release.sh 1.1.0 --dry-run
#
# Pushing the tag is what builds and signs the images and creates the GitHub
# release; see docs/releasing.md. So everything that could be wrong is checked
# before the tag exists, not after.
set -euo pipefail

cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

RED=$'\e[31m'; GREEN=$'\e[32m'; RESET=$'\e[0m'
[[ -t 1 ]] || { RED=""; GREEN=""; RESET=""; }
die() { printf '%serror:%s %s\n' "$RED" "$RESET" "$*" >&2; exit 1; }
ok()  { printf '  %s✓%s %s\n' "$GREEN" "$RESET" "$*"; }

VERSION="${1:-}"
DRY_RUN=false
[[ "${2:-}" == "--dry-run" ]] && DRY_RUN=true

[[ -n "$VERSION" ]] || die "usage: $0 X.Y.Z [--dry-run]"
# Without the leading v, because that is how it is written in the changelog;
# the tag gets it added.
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
  die "version must be X.Y.Z (no leading v, no suffix); got '$VERSION'"
TAG="v$VERSION"

git rev-parse --git-dir >/dev/null 2>&1 || die "not a git checkout"

# 1. The tag must not exist, locally or on the remote. A tag that is already
#    pushed has already built and signed an image; moving it would leave two
#    different builds claiming the same version.
git rev-parse -q --verify "refs/tags/$TAG" >/dev/null &&
  die "$TAG already exists locally; releases are not re-cut"
git fetch --quiet --tags || die "could not reach the git remote"
if git ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null 2>&1; then
  die "$TAG is already released; bump to the next version instead"
fi
ok "$TAG is new"

# 2. A dirty tree means the tag would not describe what gets built.
[[ -z "$(git status --porcelain)" ]] || {
  git status --short
  die "there are uncommitted changes; commit them first"
}
ok "the tree is clean"

# 3. The branch must be pushed, or the tag points at a commit nobody else has
#    and the release workflow checks out something different.
upstream="$(git rev-parse --abbrev-ref '@{u}' 2>/dev/null)" ||
  die "this branch has no upstream; push it first"
[[ "$(git rev-parse HEAD)" == "$(git rev-parse '@{u}')" ]] ||
  die "HEAD differs from $upstream; push or pull first"
ok "level with $upstream"

# 4. The changelog section is the release notes, so it has to be written
#    before the tag rather than remembered afterwards.
[[ -f CHANGELOG.md ]] || die "CHANGELOG.md is missing"
grep -qE "^## ${VERSION//./\\.} " CHANGELOG.md ||
  die "CHANGELOG.md has no '## $VERSION — <date>' section; write it first"
# Today's date, so a section written days ago is not published with a stale one.
today="$(date -u +%Y-%m-%d)"
grep -qE "^## ${VERSION//./\\.} .* ${today}\$" CHANGELOG.md ||
  printf '  ! the %s section is not dated %s; check it is the date you want\n' "$VERSION" "$today"
ok "CHANGELOG.md documents $VERSION"

# 5. The version has to move forwards. Comparing with sort -V keeps 1.10.0
#    after 1.9.0, which a string comparison would not.
previous="$(git tag --list 'v[0-9]*' | sed 's/^v//' | sort -V | tail -1)"
if [[ -n "$previous" ]]; then
  [[ "$(printf '%s\n%s\n' "$previous" "$VERSION" | sort -V | tail -1)" == "$VERSION" ]] ||
    die "$VERSION is not after the latest release ($previous)"
  ok "$previous → $VERSION"
fi

printf '\n%s\n' "$(sed -n "/^## ${VERSION//./\\.} /,/^## /p" CHANGELOG.md | sed '$ { /^## /d; }')"

if [[ "$DRY_RUN" == true ]]; then
  printf '%s✓%s every check passed; --dry-run, so no tag was created\n' "$GREEN" "$RESET"
  exit 0
fi

# The tag message is the changelog section, so `git show v1.1.0` says what
# changed without a network round trip.
notes="$(sed -n "/^## ${VERSION//./\\.} /,/^## /p" CHANGELOG.md | sed '$ { /^## /d; }')"
git tag --annotate "$TAG" --message "$TAG"$'\n\n'"$notes"
git push origin "$TAG"

printf '\n%s✓%s %s pushed. The release workflow is building and signing the images:\n' \
  "$GREEN" "$RESET" "$TAG"
printf '    %s/actions\n' "$(git remote get-url origin | sed 's/\.git$//;s#git@github.com:#https://github.com/#')"
