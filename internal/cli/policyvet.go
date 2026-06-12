package cli

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"m31labs.dev/tiller/internal/policy"
)

// vendorStrayRe matches vendor model tokens that should not appear in default policies.
var vendorStrayRe = regexp.MustCompile(`\b(fable|opus|sonnet|haiku)\b`)

var policyKinds = []string{"dispatch", "toolgate", "ambient"}

// runPolicy implements `tiller policy <subcommand>`.
func runPolicy(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tiller policy vet")
	}
	switch args[0] {
	case "vet":
		return policyVet()
	default:
		return fmt.Errorf("unknown policy subcommand %q (want: vet)", args[0])
	}
}

// policyVet compiles and schema-typechecks all policies, printing their
// sha256 hashes on success. When the `arbiter` CLI is present on PATH it
// additionally runs `arbiter test` on the .test.arb suites and
// `arbiter check --go <schemas.go> --type <T>` for each policy.
// Exits non-zero on any compile or test failure.
// When arbiter is absent, skips extended checks with a warning.
func policyVet() error {
	projectDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}

	allOK := true
	for _, kind := range policyKinds {
		loaded, err := policy.Load(kind, projectDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "policy vet: %v\n", err)
			allOK = false
			continue
		}
		fmt.Printf("%s  %s (%s)\n", loaded.SHA256, kind+".arb", loaded.Path)

		// Vendor-stray detector: fail if any default policy contains bare vendor
		// model tokens (fable|opus|sonnet|haiku). Project-local overrides are exempt.
		if loaded.Path == "embedded:"+kind {
			src, readErr := policy.EmbeddedDefaultSource(kind)
			if readErr == nil && vendorStrayRe.Match(src) {
				locs := vendorStrayRe.FindAllIndex(src, -1)
				fmt.Fprintf(os.Stderr, "policy vet: %s.arb contains vendor model token(s) at %d location(s); remove all fable|opus|sonnet|haiku references\n", kind, len(locs))
				allOK = false
			}
		}
	}
	if !allOK {
		return fmt.Errorf("policy vet failed")
	}

	// Extended checks require the arbiter CLI.
	arbiterPath, lookErr := exec.LookPath("arbiter")
	if lookErr != nil {
		fmt.Fprintf(os.Stderr, "policy vet: arbiter CLI not found on PATH — skipping arbiter test and check steps\n")
		return nil
	}

	// Materialize policy files to a temp dir so arbiter test can find the
	// bundle alongside its .test.arb file (arbiter derives bundle path by
	// stripping the .test.arb suffix).
	tmpDir, schemaFile, arbFiles, testFiles, cleanup, err := materializePolicyFiles(projectDir)
	_ = tmpDir
	if err != nil {
		return fmt.Errorf("policy vet: prepare files: %w", err)
	}
	defer cleanup()

	// Run `arbiter test <kind.test.arb> --coverage --threshold 90` for each policy.
	for _, kind := range policyKinds {
		testFile, ok := testFiles[kind]
		if !ok || testFile == "" {
			fmt.Fprintf(os.Stderr, "policy vet: no .test.arb for %s — skipping arbiter test\n", kind)
			continue
		}
		fmt.Printf("running arbiter test: %s\n", testFile)
		if err := runArbiterTest(arbiterPath, testFile); err != nil {
			fmt.Fprintf(os.Stderr, "policy vet: arbiter test %s.test.arb: %v\n", kind, err)
			allOK = false
		}
	}

	// Run `arbiter check <arb> --go <schemas.go> --type <T>` for each policy.
	typeNames := map[string]string{
		"dispatch": "DispatchRequest",
		"toolgate": "ToolCallRequest",
		"ambient":  "ToolCallRequest",
	}
	for _, kind := range policyKinds {
		arbFile, ok := arbFiles[kind]
		if !ok || arbFile == "" {
			continue
		}
		typeName := typeNames[kind]
		fmt.Printf("running arbiter check: %s --type %s\n", filepath.Base(arbFile), typeName)
		if err := runArbiterCheck(arbiterPath, schemaFile, typeName, arbFile); err != nil {
			fmt.Fprintf(os.Stderr, "policy vet: arbiter check %s.arb: %v\n", kind, err)
			allOK = false
		}
	}

	if !allOK {
		return fmt.Errorf("policy vet failed")
	}
	return nil
}

// runArbiterTest runs `arbiter test <file> --coverage --threshold 90`.
func runArbiterTest(arbiterPath, testFile string) error {
	cmd := exec.Command(arbiterPath, "test", testFile, "--coverage", "--threshold", "90")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("arbiter test failed: %w", err)
	}
	return nil
}

// runArbiterCheck runs `arbiter check <arb> --go <schemaFile> --type <typeName>`.
func runArbiterCheck(arbiterPath, schemaFile, typeName, arbFile string) error {
	cmd := exec.Command(arbiterPath, "check", arbFile, "--go", schemaFile, "--type", typeName)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		fmt.Print(out.String())
		return fmt.Errorf("check failed: %w", err)
	}
	return nil
}

// materializePolicyFiles resolves arb, test.arb, and schemas.go for all
// policies into a single temp directory. Project-local files under
// .tiller/policy/ take precedence; embedded defaults are copied otherwise.
// Returns (tmpDir, schemaFile, arbFiles, testFiles, cleanup, error).
func materializePolicyFiles(projectDir string) (
	tmpDir string,
	schemaFile string,
	arbFiles map[string]string,
	testFiles map[string]string,
	cleanup func(),
	err error,
) {
	arbFiles = make(map[string]string)
	testFiles = make(map[string]string)

	dir, err := os.MkdirTemp("", "tiller-vet-*")
	if err != nil {
		return "", "", nil, nil, nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup = func() { os.RemoveAll(dir) }

	// Materialize schemas.go.
	schemasBytes, err := policy.EmbeddedSchemasGo()
	if err != nil {
		cleanup()
		return "", "", nil, nil, nil, fmt.Errorf("read embedded schemas.go: %w", err)
	}
	schemaFile = filepath.Join(dir, "schemas.go")
	if err := os.WriteFile(schemaFile, schemasBytes, 0644); err != nil {
		cleanup()
		return "", "", nil, nil, nil, fmt.Errorf("write schemas.go: %w", err)
	}

	defaults := policy.EmbeddedDefaults()

	for _, kind := range policyKinds {
		// arb file: prefer project-local, fall back to embedded.
		localArb := filepath.Join(projectDir, ".tiller", "policy", kind+".arb")
		if data, readErr := os.ReadFile(localArb); readErr == nil {
			dst := filepath.Join(dir, kind+".arb")
			if wErr := os.WriteFile(dst, data, 0644); wErr != nil {
				cleanup()
				return "", "", nil, nil, nil, wErr
			}
			arbFiles[kind] = dst
		} else {
			data, readErr := fs.ReadFile(defaults, kind+".arb")
			if readErr != nil {
				cleanup()
				return "", "", nil, nil, nil, fmt.Errorf("read embedded %s.arb: %w", kind, readErr)
			}
			dst := filepath.Join(dir, kind+".arb")
			if wErr := os.WriteFile(dst, data, 0644); wErr != nil {
				cleanup()
				return "", "", nil, nil, nil, wErr
			}
			arbFiles[kind] = dst
		}

		// test.arb file: prefer project-local, fall back to embedded.
		localTest := filepath.Join(projectDir, ".tiller", "policy", kind+".test.arb")
		if data, readErr := os.ReadFile(localTest); readErr == nil {
			dst := filepath.Join(dir, kind+".test.arb")
			if wErr := os.WriteFile(dst, data, 0644); wErr != nil {
				cleanup()
				return "", "", nil, nil, nil, wErr
			}
			testFiles[kind] = dst
		} else if data, readErr := fs.ReadFile(defaults, kind+".test.arb"); readErr == nil {
			dst := filepath.Join(dir, kind+".test.arb")
			if wErr := os.WriteFile(dst, data, 0644); wErr != nil {
				cleanup()
				return "", "", nil, nil, nil, wErr
			}
			testFiles[kind] = dst
		}
		// If no test file found, testFiles[kind] remains empty string — caller skips.
	}

	return dir, schemaFile, arbFiles, testFiles, cleanup, nil
}
