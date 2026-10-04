// Package winres gives frost.exe a version resource and a manifest, so
// Windows can tell what it is. go generate builds versioninfo.json and
// frost.exe.manifest into .syso files here, and the linker adds them to
// frost.exe. The .syso files are gitignored. scripts/release.sh builds them
// with the release's version, and a local build without them still works.
package winres

//go:generate go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 -64 -arm=false -o rsrc_windows_amd64.syso
//go:generate go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.7.0 -64 -arm -o rsrc_windows_arm64.syso
