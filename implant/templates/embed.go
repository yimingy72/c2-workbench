package templates

import _ "embed"

//go:embed windows-x64.exe
var WindowsX64 []byte

//go:embed linux-amd64
var LinuxAmd64 []byte

//go:embed linux-arm64
var LinuxArm64 []byte

//go:embed darwin-amd64
var DarwinAmd64 []byte

//go:embed darwin-arm64
var DarwinArm64 []byte

func Lookup(target string) []byte {
	switch target {
	case "windows-x64":
		return WindowsX64
	case "linux-amd64":
		return LinuxAmd64
	case "linux-arm64":
		return LinuxArm64
	case "darwin-amd64":
		return DarwinAmd64
	case "darwin-arm64":
		return DarwinArm64
	default:
		return nil
	}
}
