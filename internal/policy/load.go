package policy

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	arbiter "m31labs.dev/arbiter"
	"m31labs.dev/arbiter/gostruct"
	"m31labs.dev/arbiter/ir"
)

//go:embed schemas.go defaults/*
var embeddedFS embed.FS

// Loaded is the result of loading and compiling a policy file.
type Loaded struct {
	Prog   *arbiter.Program
	SHA256 string // hex sha256 of the .arb source bytes
	Path   string // the resolved source path ("embedded:<kind>" if embedded)
}

// Load compiles a policy of the given kind ("dispatch" or "toolgate").
// It prefers .tiller/policy/<kind>.arb in projectDir over the embedded
// default. The program is schema-typechecked against the matching Go struct.
func Load(kind, projectDir string) (*Loaded, error) {
	var src []byte
	var path string

	// Prefer project-local override.
	if projectDir != "" {
		candidate := filepath.Join(projectDir, ".tiller", "policy", kind+".arb")
		if data, err := os.ReadFile(candidate); err == nil {
			src = data
			path = candidate
		}
	}

	// Fall back to embedded default.
	if src == nil {
		data, err := embeddedFS.ReadFile("defaults/" + kind + ".arb")
		if err != nil {
			return nil, fmt.Errorf("load policy %s: embedded default not found: %w", kind, err)
		}
		src = data
		path = "embedded:" + kind
	}

	// Build schema from schemas.go (embedded source materialized to a temp file).
	schema, err := schemaForKind(kind)
	if err != nil {
		return nil, fmt.Errorf("load policy %s: build schema: %w", kind, err)
	}

	// Compile with schema typecheck.
	var prog *arbiter.Program
	if path == "embedded:"+kind {
		prog, err = arbiter.Compile(src, arbiter.WithInputSchema(schema))
	} else {
		prog, err = arbiter.CompileFile(path, arbiter.WithInputSchema(schema))
	}
	if err != nil {
		return nil, fmt.Errorf("load policy %s (%s): compile error: %w", kind, path, err)
	}

	sum := sha256.Sum256(src)
	return &Loaded{
		Prog:   prog,
		SHA256: hex.EncodeToString(sum[:]),
		Path:   path,
	}, nil
}

// schemaForKind materializes schemas.go to a temp file and extracts the
// input schema for the struct corresponding to kind.
func schemaForKind(kind string) (*ir.InputSchema, error) {
	typeName, err := structNameForKind(kind)
	if err != nil {
		return nil, err
	}

	// Read embedded schemas.go source.
	goSrc, err := embeddedFS.ReadFile("schemas.go")
	if err != nil {
		return nil, fmt.Errorf("read embedded schemas.go: %w", err)
	}

	// Write to a temp file (gostruct.FromStructFile requires a path).
	tmp, err := os.CreateTemp("", "tiller-schemas-*.go")
	if err != nil {
		return nil, fmt.Errorf("create temp schemas file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(goSrc); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("write temp schemas file: %w", err)
	}
	tmp.Close()

	schema, err := gostruct.FromStructFile(tmp.Name(), typeName)
	if err != nil {
		return nil, fmt.Errorf("FromStructFile(%s): %w", typeName, err)
	}
	return schema, nil
}

func structNameForKind(kind string) (string, error) {
	switch kind {
	case "dispatch":
		return "DispatchRequest", nil
	case "toolgate":
		return "ToolCallRequest", nil
	case "ambient":
		return "ToolCallRequest", nil
	default:
		return "", fmt.Errorf("unknown policy kind %q (want dispatch, toolgate, or ambient)", kind)
	}
}

// EmbeddedDefaults returns an FS view of just the defaults/ subtree.
// Used by tiller init to copy defaults into a project.
func EmbeddedDefaults() fs.ReadDirFS {
	sub, err := fs.Sub(embeddedFS, "defaults")
	if err != nil {
		panic("embedded defaults not found: " + err.Error())
	}
	return sub.(fs.ReadDirFS)
}

// EmbeddedSchemasGo returns the source bytes of the embedded schemas.go file.
// Used by policyvet to pass the file path to `arbiter check --go`.
func EmbeddedSchemasGo() ([]byte, error) {
	return embeddedFS.ReadFile("schemas.go")
}
