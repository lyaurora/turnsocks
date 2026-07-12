package proxy

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentRuntimeStateWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "turnsocks.state")
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := writeRuntimeState(path, fmt.Sprintf("turn-%d:3478", i)); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}
}
