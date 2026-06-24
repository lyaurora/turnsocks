package probe

import (
	"encoding/json"
	"errors"
	"os"
)

func ReadResults(path string) (map[string]Result, error) {
	tests := make(map[string]Result)
	if path == "" {
		return tests, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tests, nil
		}
		return tests, err
	}
	if len(raw) == 0 {
		return tests, nil
	}
	if err := json.Unmarshal(raw, &tests); err != nil {
		return tests, err
	}
	return tests, nil
}

func WriteResults(path string, tests map[string]Result) error {
	if path == "" {
		return nil
	}
	raw, err := json.MarshalIndent(tests, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
