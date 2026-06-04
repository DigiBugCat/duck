package command

// version is the duck build version. It is "dev" for local builds and is
// overridden at release time via the linker:
//
//	-ldflags "-X github.com/DigiBugCat/duck/command.version=v0.1.0"
//
// GoReleaser sets it from the git tag (see .goreleaser.yaml).
var version = "dev"

func init() {
	rootCmd.Version = version
	rootCmd.SetVersionTemplate("duck {{.Version}}\n")
}
