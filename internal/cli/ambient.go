package cli

import (
	"fmt"
	"os"

	"m31labs.dev/tiller/internal/ambientgate"
)

func runAmbient(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: tiller ambient disable|enable|status")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	switch args[0] {
	case "disable", "off":
		path, changed, err := ambientgate.Disable(cwd)
		if err != nil {
			return err
		}
		if changed {
			fmt.Printf("tiller: ambient disabled for %s (%s)\n", cwd, path)
		} else {
			fmt.Printf("tiller: ambient already disabled for %s (%s)\n", cwd, path)
		}
		return nil

	case "enable", "on":
		path, changed, err := ambientgate.Enable(cwd)
		if err != nil {
			return err
		}
		if changed {
			fmt.Printf("tiller: ambient enabled for %s\n", cwd)
		} else {
			fmt.Printf("tiller: ambient already enabled for %s (%s absent)\n", cwd, path)
		}
		return nil

	case "status":
		path := ambientgate.DisabledPath(cwd)
		if ambientgate.IsDisabled(cwd) {
			fmt.Printf("tiller: ambient disabled for %s (%s)\n", cwd, path)
		} else {
			fmt.Printf("tiller: ambient enabled for %s (%s absent)\n", cwd, path)
		}
		return nil

	default:
		return fmt.Errorf("unknown ambient command %q (want disable, enable, or status)", args[0])
	}
}
