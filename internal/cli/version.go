package cli

// Version is the current tiller version.
// It can be overridden at build time via:
//
//	go build -ldflags "-X m31labs.dev/tiller/internal/cli.Version=<tag>" ./cmd/tiller
var Version = "0.4.0"
