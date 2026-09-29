package update

// releaseKey signs every release's checksums.txt. It must match
// install/release-signing.pub and RELEASE_KEY in install/install.sh.
// scripts/release.sh --setup-key writes all three, and a test checks them.
const releaseKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHO/g64dTbRW3poi7pdiPKgljYdHXG+TZeZQg4cfzAsV"
