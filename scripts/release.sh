#!/usr/bin/env bash
# Builds and signs frost packages. Only maintainers run this script.
#
#   scripts/release.sh --release v0.1.0
#   scripts/release.sh --pre-release latest
#   scripts/release.sh --dry-run latest
#   scripts/release.sh --setup-key
set -euo pipefail
cd "$(dirname "$0")/.."

repo='rhymeswithlimo/frost'
root="$(pwd)"
key="${FROST_SIGNING_KEY:-$HOME/.ssh/frost-release}"
pubfile='install/release-signing.pub'
installer='install/install.sh'
appkey='src/platform/signature.ts'
targets='darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64'
mode="${1:-}"

die() { printf 'release.sh: %s\n' "$*" >&2; exit 1; }
has() { command -v "$1" >/dev/null 2>&1; }
usage() {
  printf '%s\n' \
    'Usage: scripts/release.sh <mode> <version>' \
    '  --release       build, sign, test and publish a release' \
    '  --pre-release   build, sign, test and publish a pre-release' \
    '  --dry-run       build and sign locally, publish nothing' \
    '  --setup-key     create or reuse the release signing key' \
    'Use a changelog version such as v0.1.0, or latest.'
}
case "$mode" in
  --release | --pre-release | --dry-run | --setup-key) ;;
  -h | --help | '') usage; exit 0 ;;
  *) usage >&2; exit 2 ;;
esac

# Keep the three public copies together. Never replace an existing private key.
if [ "$mode" = --setup-key ]; then
  [ "$#" -eq 1 ] || die '--setup-key takes no version'
  has ssh-keygen || die 'ssh-keygen is missing'
  if [ ! -e "$key" ]; then
    mkdir -p "$(dirname "$key")"
    printf '%s\n' 'Creating the release signing key. Choose a passphrase.'
    (umask 077; ssh-keygen -t ed25519 -f "$key" -C 'frost release signing key')
  fi
  [ -f "$key" ] && [ -f "$key.pub" ] || die 'The private key and its .pub file are both required'
  public="$(awk 'NR == 1 { print $1, $2 }' "$key.pub")"
  [[ "$public" =~ ^ssh-ed25519\ [A-Za-z0-9+/]+={0,2}$ ]] || die 'The release key must be ed25519'
  ssh-keygen -l -f "$key.pub" >/dev/null || die 'The public key is invalid'
  [ "$public" = "$(ssh-keygen -y -f "$key")" ] || die 'The public key does not match the private key'
  [ -f "$installer" ] && [ -f "$appkey" ] || die 'The installer or application key is missing'
  [ "$(awk '/^RELEASE_KEY=/ { n++ } END { print n+0 }' "$installer")" = 1 ] || die 'The installer key declaration is ambiguous'
  [ "$(awk '/^export const releaseKey = / { n++ } END { print n+0 }' "$appkey")" = 1 ] || die 'The application key declaration is ambiguous'
  temporary="$(mktemp -d)"
  trap 'rm -rf "$temporary"' EXIT
  printf '%s\n' "$public" > "$temporary/public"
  awk -v k="$public" '/^RELEASE_KEY=/ { print "RELEASE_KEY=\"" k "\""; next } { print }' "$installer" > "$temporary/installer"
  awk -v k="$public" '/^export const releaseKey = / { print "export const releaseKey = \047" k "\047;"; next } { print }' "$appkey" > "$temporary/application"
  cat "$temporary/public" > "$pubfile"
  cat "$temporary/installer" > "$installer"
  cat "$temporary/application" > "$appkey"
  printf 'Public key updated in %s, %s and %s.\n' "$pubfile" "$installer" "$appkey"
  printf 'Back up %s securely, then commit the three public copies.\n' "$key"
  exit 0
fi

[ "$#" -eq 2 ] || { usage >&2; exit 2; }
# Resolve the version and its release notes from the changelog.
version="${2:-}"
if [ "$version" = latest ]; then
  version="$(awk '$1 == "##" && $2 ~ /^v[0-9]/ { print $2; exit }' docs/CHANGELOG.md)"
fi
[[ "$version" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$ ]] || die 'Invalid release version'
notes="$(awk -v v="$version" '$1 == "##" && $2 == v { found=1; print; next } found && /^## / { exit } found { print }' docs/CHANGELOG.md)"
[ -n "$notes" ] || die 'This version is missing from docs/CHANGELOG.md'
printf '%s\n' "$notes" | awk '/^[[:space:]]*- / { found=1 } END { exit !found }' || die 'The release notes have no changes'

# Tools, the signing key and its three public copies, and the pinned runtime.
for tool in node npm git ssh-keygen shellcheck; do has "$tool" || die "Missing $tool"; done
if [ "$mode" != --dry-run ]; then has gh || die 'Missing gh'; fi
[ -f "$key" ] && [ -f "$key.pub" ] || die 'Release signing key is missing; run --setup-key'
public="$(awk 'NR == 1 { print $1, $2 }' "$key.pub")"
[ "$public" = "$(cat "$pubfile")" ] || die 'Release signing key differs from the pinned key'
[ "$public" = "$(sed -n 's/^RELEASE_KEY="\(.*\)"$/\1/p' "$installer")" ] || die 'Installer release key differs from the pinned key'
[ "$public" = "$(sed -n "s/^export const releaseKey = '\(.*\)';$/\1/p" "$appkey")" ] || die 'Application release key differs from the pinned key'
node --input-type=module -e 'import {readFileSync} from "node:fs"; const expected=JSON.parse(readFileSync("tools/runtime-lock.json","utf8")).version; if(process.version!==expected) throw new Error("Use the pinned release runtime "+expected);'

# Publishing needs a signed-in gh, a clean pushed main and an unused version.
if [ "$mode" != --dry-run ]; then
  gh auth status >/dev/null 2>&1 || die 'Sign in with gh auth login before publishing'
  ! gh release view "$version" --repo "$repo" >/dev/null 2>&1 || die 'This release already exists'
  [ -z "$(git status --porcelain)" ] || die 'The working tree must be clean'
  [ "$(git branch --show-current)" = main ] || die 'Releases must come from main'
  git fetch -q origin main
  [ "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)" ] || die 'main must be pushed before releasing'
  ! git rev-parse -q --verify "refs/tags/$version" >/dev/null || die 'This tag already exists locally'
  remote_tag="$(git ls-remote --tags origin "refs/tags/$version")" || die 'The remote tag check failed'
  [ -z "$remote_tag" ] || die 'This tag already exists remotely'
fi

# The same checks CI runs, from a clean install.
npm ci --ignore-scripts
npm run audit:dependencies
npm run format:check
npm run check
npm test
npm audit
for script in install/install.sh scripts/release.sh; do bash -n "$script"; done
shellcheck install/install.sh scripts/release.sh
[ -t 0 ] || die 'A terminal is required to confirm the build'
printf 'Build and sign frost %s? [y/N] ' "$version"
IFS= read -r answer
case "$answer" in y | Y) ;; *) exit 0 ;; esac
# Build and audit a package per target. package.mjs prints each archive's checksums.txt line.
out="$root/releases/$version"
[ ! -e "$out" ] || die 'The release output folder already exists'
mkdir -p "$out"
: > "$out/checksums.txt"
for target in $targets; do
  node tools/package.mjs --version "$version" --platform "$target" --out "$out" >> "$out/checksums.txt"
done

# Sign checksums.txt and verify the signature against the pinned key.
ssh-keygen -Y sign -f "$key" -n file "$out/checksums.txt"
signers="$(mktemp)"
trap 'rm -f "$signers"' EXIT
printf 'frost-release %s\n' "$public" > "$signers"
ssh-keygen -Y verify -f "$signers" -I frost-release -n file -s "$out/checksums.txt.sig" < "$out/checksums.txt"
printf '%s\n' "$notes" > "$out/release-notes.md"

if [ "$mode" = --dry-run ]; then
  printf 'Files are in %s. Nothing was published.\n' "$out"
  exit 0
fi

# Publish the packages, checksums and signature to GitHub.
flags=()
[ "$mode" != --pre-release ] || flags+=(--prerelease)
gh release create "$version" --repo "$repo" --target "$(git rev-parse HEAD)" --title "frost $version" \
  --notes-file "$out/release-notes.md" "${flags[@]}" "$out"/frost_* "$out/checksums.txt" "$out/checksums.txt.sig"
