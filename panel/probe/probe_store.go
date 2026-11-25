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
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}
