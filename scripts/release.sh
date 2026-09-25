#!/usr/bin/env bash
# Builds and publishes a frost release. Maintainer only.
#
#   scripts/release.sh --release      v0.1.0    publish a release
#   scripts/release.sh --pre-release  latest    publish a pre-release
#   scripts/release.sh --dry-run      latest    check and build, publish nothing
#   scripts/release.sh --setup-key              create the signing key (once)
#
# The version (or "latest", the newest one) comes from docs/CHANGELOG.md, and
# that version's section becomes the release notes. Everything is checked
# first, then you confirm, then it builds into releases/<version>/, writes
# checksums.txt, signs it as checksums.txt.sig and publishes with gh.
#
# File names must stay in sync with install/install.sh.
set -euo pipefail

REPO="rhymeswithlimo/frost"
NAME="frost"
CHANGELOG="docs/CHANGELOG.md"
TARGETS="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 linux/armv7 windows/amd64 windows/arm64"
KEY="${FROST_SIGNING_KEY:-$HOME/.ssh/frost-release}"
PUBFILE="install/release-signing.pub"
INSTALLER="install/install.sh"
SIGNER="frost-release" # identity used in allowed_signers, must match install.sh
LDFLAG_VERSION="github.com/rhymeswithlimo/frost/internal/cli.Version"

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

# ---- look ----

if [ -t 1 ]; then
  B=$'\033[1m' D=$'\033[2m' G=$'\033[32m' R=$'\033[31m' Y=$'\033[33m' C=$'\033[36m' X=$'\033[0m'
else
  B="" D="" G="" R="" Y="" C="" X=""
fi
W=72

repeat() { # repeat <string> <count>
  local s="" i
  for ((i = 0; i < $2; i++)); do s+="$1"; done
  printf '%s' "$s"
}

header() { # header <right label>
  local left=" frost release " right=" $1 "
  local fill=$((W - 2 - ${#left} - ${#right}))
  printf '%s┌%s%s%s%s%s┐%s\n' "$C" "$B" "$left" "$X$C" "$(repeat ─ "$fill")" "$B$right$X$C" "$X"
  printf '%s└%s┘%s\n' "$C" "$(repeat ─ $((W - 2)))" "$X"
}

section() { printf '\n%s%s%s\n' "$B" "$1" "$X"; }
field() { printf '  %s%-10s%s %s\n' "$D" "$1" "$X" "$2"; }
say() { printf '%s\n' "$*"; }
die() {
  printf '\n%s✗ %s%s\n' "$R" "$*" "$X" >&2
  exit 1
}

usage() {
  cat <<EOF
Usage: scripts/release.sh <mode> <version>

Modes:
  --release       build and publish a release
  --pre-release   build and publish a pre-release
  --dry-run       run the checks and build locally, publish nothing
  --setup-key     create the release signing key (one time, no version)

Version:
  v0.1.0          a version listed in $CHANGELOG
  latest          the newest version listed there
EOF
}

# ---- arguments ----

MODE="${1:-}"
case "$MODE" in
  --release | --pre-release | --dry-run | --setup-key) ;;
  -h | --help | "")
    usage
    exit 0
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

# ---- changelog ----

versions() { awk '$1 == "##" && $2 ~ /^v[0-9]/ { print $2 }' "$CHANGELOG"; }

# notes <version>: that version's heading and everything up to the next one.
notes() {
  awk -v v="$1" '
    $1 == "##" && $2 == v { found = 1; print; next }
    found && /^## / { exit }
    found { print }
  ' "$CHANGELOG" | awk '{ lines[NR] = $0 } END { n = NR; while (n > 0 && lines[n] ~ /^[[:space:]]*$/) n--; for (i = 1; i <= n; i++) print lines[i] }'
}

# ---- signing key setup ----

setup_key() {
  header "SIGNING KEY"
  section "Release signing key"
  field "Private" "$KEY"
  field "Public" "$PUBFILE, and RELEASE_KEY in $INSTALLER"
  say ""
  if [ -f "$KEY" ]; then
    say "  A key already exists at $KEY. Using it."
  else
    say "  Creating a new ed25519 key. Pick a passphrase: you'll type it for each release."
    say ""
    ssh-keygen -t ed25519 -f "$KEY" -C "frost release signing key" || die "ssh-keygen failed"
  fi
  [ -f "$KEY.pub" ] || die "$KEY.pub is missing. Recreate it with: ssh-keygen -y -f $KEY > $KEY.pub"

  local pub
  pub="$(awk '{ print $1, $2 }' "$KEY.pub")"
  printf '%s\n' "$pub" >"$PUBFILE"
  local tmp
  tmp="$(mktemp)"
  awk -v k="$pub" '/^RELEASE_KEY=/ { print "RELEASE_KEY=\"" k "\""; next } { print }' "$INSTALLER" >"$tmp"
  cat "$tmp" >"$INSTALLER"
  rm -f "$tmp"

  section "Done"
  field "Fingerprint" "$(ssh-keygen -l -f "$KEY.pub" | awk '{ print $2 }')"
  say ""
  say "  Next:"
  say "  1. Commit $PUBFILE and $INSTALLER."
  say "  2. Back up $KEY somewhere safe. Without it you can't sign releases"
  say "     that existing installs will trust."
  exit 0
}

[ "$MODE" = "--setup-key" ] && setup_key

VERSION_ARG="${2:-}"
[ -n "$VERSION_ARG" ] || {
  usage >&2
  exit 2
}
[ -f "$CHANGELOG" ] || die "$CHANGELOG not found"

if [ "$VERSION_ARG" = "latest" ]; then
  VERSION="$(versions | head -n 1)"
  [ -n "$VERSION" ] || die "no versions (## vX.Y.Z headings) in $CHANGELOG"
else
  VERSION="v${VERSION_ARG#v}"
fi
NUM="${VERSION#v}"
OUT="$ROOT/releases/$VERSION"
NOTES="$(notes "$VERSION")"

case "$MODE" in
  --release) KIND="release" LABEL="RELEASE" ;;
  --pre-release) KIND="pre-release" LABEL="PRE-RELEASE" ;;
  --dry-run) KIND="dry run" LABEL="DRY RUN" ;;
esac
DRY=false
[ "$MODE" = "--dry-run" ] && DRY=true

artifact() { # artifact <os/arch>: the file name for a target
  local os="${1%/*}" arch="${1#*/}" ext="tar.gz"
  [ "$os" = windows ] && ext="zip"
  printf '%s_%s_%s_%s.%s' "$NAME" "$NUM" "$os" "$arch" "$ext"
}

# ---- details ----

clear 2>/dev/null || true
header "$LABEL"

section "Details"
field "Release" "$NAME $VERSION"
case "$KIND" in
  release) field "Type" "release" ;;
  pre-release) field "Type" "pre-release (marked as not ready for production)" ;;
  "dry run") field "Type" "dry run: builds locally, publishes nothing" ;;
esac
field "Commit" "$(git rev-parse --short HEAD 2>/dev/null || echo '?') on $(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo '?')"
field "Tag" "$VERSION, created on GitHub when publishing"
field "Output" "releases/$VERSION/"

section "Files"
for t in $TARGETS; do say "  $(artifact "$t")"; done
say "  checksums.txt"
say "  checksums.txt.sig"

section "Release notes ${D}(from $CHANGELOG)${X}"
if [ -n "$NOTES" ]; then
  total="$(printf '%s\n' "$NOTES" | wc -l | tr -d ' ')"
  printf '%s\n' "$NOTES" | head -n 14 | while IFS= read -r line; do
    printf '  %s│%s %s\n' "$D" "$X" "${line:0:$((W - 6))}"
  done
  [ "$total" -gt 14 ] && printf '  %s│ ... %d more lines%s\n' "$D" $((total - 14)) "$X"
else
  say "  ${R}(none: $VERSION isn't in $CHANGELOG)${X}"
fi

# ---- checks ----
#
# Each check prints one line of detail and returns 0 (pass), 1 (fail) or
# 2 (warning). In a dry run, problems that only matter for publishing are
# warnings instead of failures.

soft() { if $DRY; then return 2; else return 1; fi; }

has() { command -v "$1" >/dev/null 2>&1; }

sha256() {
  if has sha256sum; then sha256sum "$@"; else shasum -a 256 "$@"; fi
}

c_tools() {
  local missing=""
  for t in go git tar zip ssh-keygen awk; do has "$t" || missing="$missing $t"; done
  has sha256sum || has shasum || missing="$missing sha256sum"
  [ -z "$missing" ] || {
    echo "missing:$missing"
    return 1
  }
  if ! has gh; then
    echo "gh is missing (needed to publish)"
    soft
    return
  fi
  echo "go, git, gh, tar, zip, ssh-keygen"
}

c_changelog() {
  [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$ ]] || {
    echo "$VERSION isn't a version like v1.2.3"
    return 1
  }
  [ -n "$NOTES" ] || {
    echo "$VERSION isn't listed in $CHANGELOG"
    return 1
  }
  if printf '%s\n' "$NOTES" | grep -qE '^[[:space:]]*-[[:space:]]*$'; then
    echo "$VERSION has empty bullets"
    return 1
  fi
  if [ "$(printf '%s\n' "$NOTES" | grep -cE '^[[:space:]]*- ')" -eq 0 ]; then
    echo "$VERSION has no bullets"
    return 1
  fi
  echo "$VERSION found, $(printf '%s\n' "$NOTES" | wc -l | tr -d ' ') lines of notes"
}

c_files() {
  for f in LICENSE README.md go.mod cmd/frost/main.go "$INSTALLER"; do
    [ -f "$f" ] || {
      echo "$f is missing"
      return 1
    }
  done
  echo "LICENSE and README.md go in every archive"
}

c_github() {
  has gh || {
    echo "gh isn't installed"
    soft
    return
  }
  gh auth status >/dev/null 2>&1 || {
    echo "not logged in, run: gh auth login"
    soft
    return
  }
  gh repo view "$REPO" --json name >/dev/null 2>&1 || {
    echo "can't reach github.com/$REPO"
    soft
    return
  }
  if gh release view "$VERSION" -R "$REPO" >/dev/null 2>&1; then
    echo "a GitHub release for $VERSION already exists"
    soft
    return
  fi
  echo "logged in, github.com/$REPO reachable, no $VERSION release yet"
}

# newer <a> <b>: is version a newer than b? Pre-release suffixes are ignored.
newer() {
  local a="${1#v}" b="${2#v}"
  a="${a%%-*}" b="${b%%-*}"
  local IFS=.
  # shellcheck disable=SC2206
  local x=($a) y=($b) i
  for i in 0 1 2; do
    [ "${x[i]:-0}" -gt "${y[i]:-0}" ] && return 0
    [ "${x[i]:-0}" -lt "${y[i]:-0}" ] && return 1
  done
  [ "$1" != "$2" ] && [[ "$2" == *-* ]] && [[ "$1" != *-* ]] # v1.0.0 beats v1.0.0-rc1
}

c_tag() {
  if git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null; then
    echo "tag $VERSION already exists locally"
    soft
    return
  fi
  local remote
  remote="$(git ls-remote --tags origin 2>/dev/null)" || {
    echo "can't reach the git remote"
    soft
    return
  }
  if printf '%s\n' "$remote" | grep -q "refs/tags/$VERSION\$"; then
    echo "tag $VERSION already exists on GitHub"
    soft
    return
  fi
  local latest="" t
  for t in $(printf '%s\n' "$remote" | sed -n 's|.*refs/tags/\(v[0-9][^^]*\)$|\1|p'); do
    if [ -z "$latest" ] || newer "$t" "$latest"; then latest="$t"; fi
  done
  if [ -n "$latest" ] && ! newer "$VERSION" "$latest"; then
    echo "$VERSION isn't newer than the latest tag, $latest"
    soft
    return
  fi
  echo "$VERSION is new${latest:+ (latest tag is $latest)}"
}

c_git() {
  if [ -n "$(git status --porcelain)" ]; then
    echo "uncommitted changes: the release would include them"
    soft
    return
  fi
  git fetch -q origin 2>/dev/null || {
    echo "can't fetch from origin"
    soft
    return
  }
  if [ -z "$(git branch -r --contains HEAD 2>/dev/null)" ]; then
    echo "$(git rev-parse --short HEAD) isn't pushed to GitHub yet"
    soft
    return
  fi
  local branch
  branch="$(git rev-parse --abbrev-ref HEAD)"
  if [ "$branch" != "main" ]; then
    echo "clean and pushed, but on $branch, not main"
    return 2
  fi
  echo "clean, on main, pushed"
}

c_tests() {
  go vet ./... >/dev/null 2>&1 || {
    echo "go vet failed, run it to see why"
    return 1
  }
  go test ./... >/dev/null 2>&1 || {
    echo "tests failed, run: go test ./..."
    return 1
  }
  echo "go vet and go test ./... pass"
}

c_key() {
  if [ ! -f "$KEY" ] || [ ! -f "$KEY.pub" ]; then
    echo "no signing key at $KEY, run: scripts/release.sh --setup-key"
    soft
    return
  fi
  local pub committed embedded
  pub="$(awk '{ print $1, $2 }' "$KEY.pub")"
  committed="$(awk '{ print $1, $2 }' "$PUBFILE" 2>/dev/null || true)"
  embedded="$(sed -n 's/^RELEASE_KEY="\(.*\)"$/\1/p' "$INSTALLER")"
  if [ "$pub" != "$committed" ] || [ "$pub" != "$embedded" ]; then
    echo "$KEY doesn't match $PUBFILE and $INSTALLER, run --setup-key"
    return 1
  fi
  ssh-keygen -l -f "$KEY.pub" | awk '{ print $2 }'
}

LOGDIR="$(mktemp -d)"
trap 'rm -rf "$LOGDIR"' EXIT
FAILS=0
WARNS=0
SIGN=true

check() { # check <label> <function>
  local label="$1" fn="$2" log="$LOGDIR/$2" rc=0 i=0 frames="|/-\\"
  ("$fn" >"$log" 2>&1) &
  local pid=$!
  while kill -0 "$pid" 2>/dev/null; do
    [ -t 1 ] && printf '\r  %s%s%s %s' "$C" "${frames:$((i++ % 4)):1}" "$X" "$label"
    sleep 0.1
  done
  wait "$pid" || rc=$?
  local detail
  detail="$(tail -n 1 "$log")"
  case "$rc" in
    0) printf '\r\033[K  %s✓%s %-16s %s%s%s\n' "$G" "$X" "$label" "$D" "$detail" "$X" ;;
    2)
      printf '\r\033[K  %s!%s %-16s %s\n' "$Y" "$X" "$label" "$detail"
      WARNS=$((WARNS + 1))
      ;;
    *)
      printf '\r\033[K  %s✗%s %-16s %s\n' "$R" "$X" "$label" "$detail"
      FAILS=$((FAILS + 1))
      ;;
  esac
  if [ "$fn" = c_key ] && [ "$rc" -ne 0 ]; then SIGN=false; fi
}

section "Checks"
check "Tools" c_tools
check "Changelog" c_changelog
check "Files" c_files
check "GitHub" c_github
check "Tag" c_tag
check "Git" c_git
check "Signing key" c_key
check "Tests" c_tests

if [ "$FAILS" -gt 0 ]; then
  printf '\n%s✗ %d check(s) failed. Nothing was built or published.%s\n' "$R" "$FAILS" "$X"
  exit 1
fi

# ---- confirm ----

confirm() { # confirm <question> <yes label>
  local inner=$((W - 4))
  local q="${1:0:$inner}" yes="$2"
  printf '\n%s┌%s┐%s\n' "$C" "$(repeat ─ $((W - 2)))" "$X"
  printf '%s│%s %s%-*s%s %s│%s\n' "$C" "$X" "$B" "$inner" "$q" "$X" "$C" "$X"
  local hint
  hint="[y] $yes    [n] cancel"
  [ "$WARNS" -gt 0 ] && hint="$hint    ($WARNS warning(s) above)"
  printf '%s│%s %-*s %s│%s\n' "$C" "$X" "$inner" "$hint" "$C" "$X"
  printf '%s└%s┘%s\n' "$C" "$(repeat ─ $((W - 2)))" "$X"
  [ -t 0 ] || die "not a terminal, can't confirm"
  local key
  while true; do
    IFS= read -rsn 1 key
    case "$key" in
      y | Y) return 0 ;;
      n | N | q | $'\e') return 1 ;;
    esac
  done
}

if $DRY; then
  q="Build $NAME $VERSION locally? Nothing will be published."
  yes="build"
else
  q="Publish $NAME $VERSION to GitHub as a $KIND?"
  yes="publish"
fi
if ! confirm "$q" "$yes"; then
  say ""
  say "Cancelled. Nothing was built or published."
  exit 0
fi

# ---- build ----

section "Build"
rm -rf "$OUT"
mkdir -p "$OUT/.stage"

for t in $TARGETS; do
  os="${t%/*}" arch="${t#*/}" goarch="${t#*/}" goarm=""
  if [ "$arch" = armv7 ]; then goarch=arm goarm=7; fi
  file="$(artifact "$t")"
  stage="$OUT/.stage/${file%%.*}"
  bin="$NAME"
  [ "$os" = windows ] && bin="$NAME.exe"
  mkdir -p "$stage"
  printf '  %s…%s %s' "$C" "$X" "$file"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$goarch" GOARM="$goarm" go build -trimpath \
    -ldflags "-s -w -X $LDFLAG_VERSION=$VERSION" -o "$stage/$bin" ./cmd/frost ||
    die "build failed for $t"
  cp LICENSE README.md "$stage/"
  if [ "$os" = windows ]; then
    (cd "$stage" && zip -qX "$OUT/$file" ./*)
  else
    COPYFILE_DISABLE=1 tar -C "$stage" -czf "$OUT/$file" .
  fi
  printf '\r\033[K  %s✓%s %-36s %s%s%s\n' "$G" "$X" "$file" "$D" "$(du -h "$OUT/$file" | awk '{ print $1 }')" "$X"
done
rm -rf "$OUT/.stage"

(cd "$OUT" && sha256 "$NAME"_* >checksums.txt)
printf '  %s✓%s %s\n' "$G" "$X" "checksums.txt"

if $SIGN; then
  say "  ${D}signing checksums.txt (ssh-keygen may ask for your key's passphrase)${X}"
  # The passphrase prompt goes to the terminal directly, so it stays visible.
  ssh-keygen -Y sign -f "$KEY" -n file "$OUT/checksums.txt" 2>"$LOGDIR/sign.log" ||
    die "signing failed: $(tail -n 1 "$LOGDIR/sign.log")"
  printf '%s %s\n' "$SIGNER" "$(cat "$PUBFILE")" >"$LOGDIR/allowed_signers"
  ssh-keygen -Y verify -f "$LOGDIR/allowed_signers" -I "$SIGNER" -n file \
    -s "$OUT/checksums.txt.sig" <"$OUT/checksums.txt" >/dev/null 2>&1 ||
    die "the new signature doesn't verify against $PUBFILE"
  printf '  %s✓%s %s\n' "$G" "$X" "checksums.txt.sig (verified)"
else
  printf '  %s!%s %s\n' "$Y" "$X" "checksums.txt.sig skipped: no signing key"
fi

printf '%s\n' "$NOTES" >"$OUT/release-notes.md"

if $DRY; then
  section "Done"
  say "  Dry run finished. Files are in releases/$VERSION/."
  say "  Nothing was published."
  exit 0
fi

# ---- publish ----

section "Publish"
flags=()
[ "$KIND" = "pre-release" ] && flags+=(--prerelease)
url="$(gh release create "$VERSION" -R "$REPO" \
  --target "$(git rev-parse HEAD)" \
  --title "$NAME $VERSION" \
  --notes-file "$OUT/release-notes.md" \
  ${flags[@]+"${flags[@]}"} \
  "$OUT"/"$NAME"_* "$OUT/checksums.txt" "$OUT/checksums.txt.sig")" ||
  die "gh release create failed. Check GitHub: a partial release or tag may need deleting."
git fetch -q --tags origin || true

printf '  %s✓%s published %s\n' "$G" "$X" "$url"
section "Done"
say "  $NAME $VERSION is live as a $KIND."
