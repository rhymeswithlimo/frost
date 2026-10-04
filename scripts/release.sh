#!/usr/bin/env bash
# Builds and publishes a frost release. Maintainer only.
#
#   scripts/release.sh --release      v0.1.0    publish a release
#   scripts/release.sh --pre-release  latest    publish a pre-release
#   scripts/release.sh --dry-run      latest    check and build, publish nothing
#   scripts/release.sh --setup-key              create the signing keys (once)
#
# The version (or "latest", the newest one) comes from docs/CHANGELOG.md, and
# that version's section becomes the release notes. Everything is checked
# first, then you confirm, then it builds into releases/<version>/ with the
# version in frost.exe's version resource and the macOS builds signed, writes
# checksums.txt and signs it as checksums.txt.sig. On Windows it then tests
# frost.exe against Microsoft Defender, logs the result to
# releases/defender-check-<version>.txt, and publishes with gh only if
# Defender didn't flag it. Elsewhere it asks before building, since Defender
# can't be tested there.
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
GOKEY="internal/update/key.go" # frost update checks releases against this copy
SIGNER="frost-release" # identity used in allowed_signers, must match install.sh
LDFLAG_VERSION="github.com/rhymeswithlimo/frost/internal/cli.Version"
WINRES_DIR="cmd/frost/winres" # frost.exe's version resource and manifest
# macOS ties Full Disk Access to the certificate a binary is signed with, so
# every macOS release is signed with the one in MACCERT. A new certificate
# would make every Mac ask again.
MACKEY="${FROST_MACOS_SIGNING_KEY:-$HOME/.ssh/frost-macos-signing.pem}" # its private key and certificate
MACCERT="scripts/macos-signing.crt"
MACID="io.github.rhymeswithlimo.frost" # the code signing identifier, same as the launchd label
RCODESIGN="${RCODESIGN:-rcodesign}"
DEFENDER_CHECK="scripts/defender-check.ps1" # tests frost.exe against Microsoft Defender
DEFENDER_SECONDS=120                         # how long it watches; Defender has acted within 20

cd "$(dirname "$0")/.."
ROOT="$(pwd)"
DTEST="$ROOT/releases/.defender-check" # where frost.exe is tested, gitignored with releases/
case "$(uname -s)" in
  MINGW* | MSYS* | CYGWIN*) HOST_OS=Windows ;;
  Darwin) HOST_OS=macOS ;;
  *) HOST_OS="$(uname -s)" ;;
esac
# The goversioninfo that builds the resource, from the go:generate line.
WINRES="$(sed -n 's|^//go:generate go run \([^ ]*/goversioninfo@[^ ]*\) .*|\1|p' "$WINRES_DIR/winres.go" 2>/dev/null | head -n 1 || true)"

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
  --setup-key     create the release signing keys (one time, no version)

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
  field "Public" "$PUBFILE, RELEASE_KEY in $INSTALLER, releaseKey in $GOKEY"
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
  awk -v k="$pub" '/^const releaseKey = / { print "const releaseKey = \"" k "\""; next } { print }' "$GOKEY" >"$tmp"
  cat "$tmp" >"$GOKEY"
  rm -f "$tmp"

  section "macOS signing certificate"
  field "Private" "$MACKEY"
  field "Public" "$MACCERT"
  say ""
  if [ -f "$MACKEY" ]; then
    say "  A certificate already exists at $MACKEY. Using it."
  else
    command -v openssl >/dev/null 2>&1 || die "openssl is missing, and it's needed to create the certificate"
    say "  Creating a self-signed certificate that lasts 20 years."
    make_mac_cert || die "creating the certificate failed"
  fi
  tr -d '\r' <"$MACKEY" | awk '/BEGIN CERTIFICATE/,/END CERTIFICATE/' >"$MACCERT"
  [ -s "$MACCERT" ] || die "$MACKEY holds no certificate"

  section "Done"
  field "Fingerprint" "$(ssh-keygen -l -f "$KEY.pub" | awk '{ print $2 }')"
  field "macOS cert" "$(openssl x509 -in "$MACCERT" -noout -fingerprint -sha256 2>/dev/null | sed 's/.*=//')"
  say ""
  say "  Next:"
  say "  1. Commit $PUBFILE, $INSTALLER, $GOKEY and $MACCERT."
  say "  2. Back up $KEY and $MACKEY somewhere safe. Without $KEY you"
  say "     can't sign releases that existing installs will trust, and a new"
  say "     macOS certificate makes every Mac ask for Full Disk Access again."
  exit 0
}

# make_mac_cert writes a new self-signed code signing certificate, and its
# private key, to MACKEY. It's named plainly after frost rather than in the
# style of Apple's own certificates.
make_mac_cert() {
  local cfg
  cfg="$(mktemp)"
  printf '%s\n' '[req]' 'distinguished_name = dn' 'prompt = no' 'x509_extensions = ext' \
    '[dn]' "CN = $NAME" '[ext]' 'keyUsage = critical, digitalSignature' \
    'extendedKeyUsage = critical, codeSigning' 'basicConstraints = critical, CA:false' \
    'subjectKeyIdentifier = hash' >"$cfg"
  mkdir -p "$(dirname "$MACKEY")"
  (umask 077 && openssl req -x509 -newkey rsa:3072 -nodes -days 7300 -config "$cfg" \
    -keyout "$MACKEY" -out "$cfg.crt" 2>"$cfg.log" && cat "$cfg.crt" >>"$MACKEY") || {
    tail -n 1 "$cfg.log" >&2
    rm -f "$cfg" "$cfg.crt" "$cfg.log" "$MACKEY"
    return 1
  }
  rm -f "$cfg" "$cfg.crt" "$cfg.log"
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

# has_utf16 <file> <text>: does the file hold text as UTF-16LE?
has_utf16() {
  local want hex
  want="$(printf '%s' "$2" | od -An -tx1 -v | tr -d ' \n' | sed 's/../&00/g')"
  hex="$(od -An -tx1 -v "$1" | tr -d ' \n')"
  [[ "$hex" == *"$want"* ]]
}

# make_winres <dir>: writes frost.exe's version resource for this version to
# dir, as the .syso files the linker picks up from $WINRES_DIR.
make_winres() {
  local major minor patch f
  IFS=. read -r major minor patch <<<"${NUM%%-*}"
  local v=(-ver-major "$major" -ver-minor "$minor" -ver-patch "$patch" -ver-build 0
    -product-ver-major "$major" -product-ver-minor "$minor" -product-ver-patch "$patch" -product-ver-build 0
    -file-version "$NUM" -product-version "$NUM")
  mkdir -p "$1"
  (cd "$WINRES_DIR" &&
    go run "$WINRES" -64 -arm=false "${v[@]}" -o "$1/rsrc_windows_amd64.syso" &&
    go run "$WINRES" -64 -arm "${v[@]}" -o "$1/rsrc_windows_arm64.syso") || return 1
  for f in "$1/rsrc_windows_amd64.syso" "$1/rsrc_windows_arm64.syso"; do
    has_utf16 "$f" "$NUM" || {
      echo "$f doesn't hold version $NUM"
      return 1
    }
  done
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
  for t in go git tar zip ssh-keygen awk od; do has "$t" || missing="$missing $t"; done
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
  for f in LICENSE README.md go.mod cmd/frost/main.go "$INSTALLER" "$GOKEY"; do
    [ -f "$f" ] || {
      echo "$f is missing"
      return 1
    }
  done
  echo "LICENSE and README.md go in every archive"
}

# c_winres builds the version resource once to a scratch folder, so a missing
# tool or a broken versioninfo.json shows up before you confirm.
c_winres() {
  [ -n "$WINRES" ] || {
    echo "no goversioninfo go:generate line in $WINRES_DIR/winres.go"
    return 1
  }
  make_winres "$LOGDIR/winres" >"$LOGDIR/winres.log" 2>&1 || {
    echo "can't build it: $(tail -n 1 "$LOGDIR/winres.log")"
    return 1
  }
  echo "frost.exe will say $NUM (${WINRES##*/})"
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
  local pub committed embedded compiled
  pub="$(awk '{ print $1, $2 }' "$KEY.pub")"
  committed="$(awk '{ print $1, $2 }' "$PUBFILE" 2>/dev/null || true)"
  embedded="$(sed -n 's/^RELEASE_KEY="\(.*\)"$/\1/p' "$INSTALLER")"
  compiled="$(sed -n 's/^const releaseKey = "\(.*\)"$/\1/p' "$GOKEY")"
  if [ "$pub" != "$committed" ] || [ "$pub" != "$embedded" ] || [ "$pub" != "$compiled" ]; then
    echo "$KEY doesn't match $PUBFILE, $INSTALLER and $GOKEY, run --setup-key"
    return 1
  fi
  ssh-keygen -l -f "$KEY.pub" | awk '{ print $2 }'
}

c_macsign() {
  has "$RCODESIGN" || {
    echo "rcodesign isn't installed, run: cargo install apple-codesign"
    soft
    return
  }
  [ -f "$MACKEY" ] || {
    echo "no macOS signing certificate at $MACKEY, run: scripts/release.sh --setup-key"
    soft
    return
  }
  [ -f "$MACCERT" ] || {
    echo "$MACCERT is missing, run: scripts/release.sh --setup-key"
    return 1
  }
  if [ "$(tr -d '\r' <"$MACKEY" | awk '/BEGIN CERTIFICATE/,/END CERTIFICATE/')" != "$(tr -d '\r' <"$MACCERT")" ]; then
    echo "$MACKEY isn't the certificate in $MACCERT, and a new one would make every Mac ask for Full Disk Access again"
    return 1
  fi
  echo "macOS builds will be signed as $MACID"
}

# defender_ps <args>: runs the Defender check with Windows PowerShell.
defender_ps() {
  powershell.exe -NoProfile -NonInteractive -ExecutionPolicy Bypass \
    -File "$(cygpath -w "$ROOT/$DEFENDER_CHECK")" "$@"
}

c_defender() {
  if [ "$HOST_OS" != Windows ]; then
    echo "frost.exe can't be tested against Windows Defender on $HOST_OS"
    return 2
  fi
  if ! has powershell.exe || ! has cygpath; then
    echo "powershell.exe or cygpath is missing, so frost.exe can't be tested against Windows Defender"
    return 2
  fi
  if defender_ps -Probe; then return 0; fi
  return 2
}

LOGDIR="$(mktemp -d)"
WINRES_MADE=false
# The build's .syso files carry this release's version, so they don't
# outlive it. A local go generate's copies are replaced by them and go too.
cleanup() {
  if $WINRES_MADE; then rm -f "$ROOT/$WINRES_DIR"/rsrc_windows_*.syso; fi
  rm -rf "$LOGDIR" "$DTEST"
}
trap cleanup EXIT
FAILS=0
WARNS=0
SIGN=true
MACSIGN=true
DEFENDER=true

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
  if [ "$fn" = c_macsign ] && [ "$rc" -ne 0 ]; then MACSIGN=false; fi
  if [ "$fn" = c_defender ] && [ "$rc" -ne 0 ]; then DEFENDER=false; fi
}

section "Checks"
check "Tools" c_tools
check "Changelog" c_changelog
check "Files" c_files
check "Version resource" c_winres
check "GitHub" c_github
check "Tag" c_tag
check "Git" c_git
check "Signing key" c_key
check "macOS signing" c_macsign
check "Defender test" c_defender
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

if ! $DEFENDER && ! confirm "frost.exe can't be tested against Windows Defender here. Continue?" "continue"; then
  say ""
  say "Cancelled. Nothing was built or published."
  exit 0
fi

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
rm -rf "$OUT" "$DTEST"
mkdir -p "$OUT/.stage"

printf '  %s…%s %s' "$C" "$X" "frost.exe version resource"
WINRES_MADE=true
make_winres "$ROOT/$WINRES_DIR" >"$LOGDIR/winres.log" 2>&1 ||
  die "building frost.exe's version resource failed: $(tail -n 1 "$LOGDIR/winres.log")"
printf '\r\033[K  %s✓%s %-36s %s%s%s\n' "$G" "$X" "frost.exe version resource" "$D" "$NUM" "$X"

for t in $TARGETS; do
  os="${t%/*}" arch="${t#*/}" goarch="${t#*/}" goarm=""
  if [ "$arch" = armv7 ]; then goarch=arm goarm=7; fi
  file="$(artifact "$t")"
  stage="$OUT/.stage/${os}_${arch}"
  bin="$NAME"
  [ "$os" = windows ] && bin="$NAME.exe"
  mkdir -p "$stage"
  printf '  %s…%s %s' "$C" "$X" "$file"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$goarch" GOARM="$goarm" go build -trimpath \
    -ldflags "-s -w -X $LDFLAG_VERSION=$VERSION" -o "$stage/$bin" ./cmd/frost ||
    die "build failed for $t"
  if [ "$os" = darwin ] && $MACSIGN; then
    "$RCODESIGN" sign --pem-file "$MACKEY" --binary-identifier "$MACID" --timestamp-url none \
      "$stage/$bin" >"$LOGDIR/macsign.log" 2>&1 ||
      die "signing failed for $t: $(tail -n 1 "$LOGDIR/macsign.log")"
    info="$("$RCODESIGN" print-signature-info "$stage/$bin" 2>/dev/null || true)"
    [[ "$info" == *"identifier: $MACID"* ]] || die "the signature on $t doesn't name $MACID"
  fi
  if [ "$t" = windows/amd64 ] && $DEFENDER; then
    mkdir -p "$DTEST" && cp "$stage/$bin" "$DTEST/$bin" # the same bytes as the zip
  fi
  cp LICENSE README.md "$stage/"
  if [ "$os" = windows ]; then
    (cd "$stage" && zip -qX "$OUT/$file" ./*)
  else
    COPYFILE_DISABLE=1 tar -C "$stage" -czf "$OUT/$file" .
  fi
  printf '\r\033[K  %s✓%s %-36s %s%s%s\n' "$G" "$X" "$file" "$D" "$(du -h "$OUT/$file" | awk '{ print $1 }')" "$X"
done
rm -rf "$OUT/.stage"
rm -f "$ROOT/$WINRES_DIR"/rsrc_windows_*.syso
WINRES_MADE=false

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

# Defender once flagged frost.exe when it turned on its scheduled backup, so
# the x64 build does that here first. Only a pass gets published.
if $DEFENDER; then
  DLOG="releases/defender-check-$VERSION.txt"
  printf '  %s…%s %s' "$C" "$X" "Windows Defender check (about 2 minutes)"
  rm -f "$ROOT/$DLOG"
  rc=0
  defender_ps -Exe "$(cygpath -w "$DTEST/$NAME.exe")" -Log "$(cygpath -w "$ROOT/$DLOG")" \
    -Seconds "$DEFENDER_SECONDS" -Version "$VERSION" >"$LOGDIR/defender.log" 2>&1 || rc=$?
  rm -rf "$DTEST"
  if [ ! -s "$ROOT/$DLOG" ]; then # PowerShell didn't get far enough to write it
    rc=2
    { echo "frost Windows Defender check: COULDN'T TEST"; echo "$DEFENDER_CHECK wrote no log. Its output:"; cat "$LOGDIR/defender.log"; } >"$ROOT/$DLOG"
  fi
  case "$rc" in
    0) printf '\r\033[K  %s✓%s %-36s %s%s%s\n' "$G" "$X" "Windows Defender check" "$D" "not flagged, log in $DLOG" "$X" ;;
    1)
      printf '\r\033[K  %s✗%s %-36s %s\n' "$R" "$X" "Windows Defender check" "flagged, log in $DLOG"
      $DRY || die "Windows Defender flagged frost.exe, so nothing was published. The details are in $DLOG"
      ;;
    *)
      printf '\r\033[K  %s✗%s %-36s %s\n' "$R" "$X" "Windows Defender check" "couldn't run, log in $DLOG"
      $DRY || die "The Windows Defender check couldn't run, so nothing was published. The details are in $DLOG"
      ;;
  esac
fi

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
