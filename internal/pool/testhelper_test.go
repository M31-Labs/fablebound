package pool

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// readJSONLFile reads a JSONL file and returns each line parsed as map[string]any.
func readJSONLFile(path string) ([]map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	var out []map[string]any
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return nil, fmt.Errorf("unmarshal: %w", err)
		}
		out = append(out, m)
	}
	return out, scanner.Err()
}
