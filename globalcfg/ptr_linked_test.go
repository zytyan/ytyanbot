package g

import (
	"sync"
	"testing"
)

func TestPtrLinkedCfgConcurrentGetAndReload(t *testing.T) {
	original := GetConfig()
	t.Cleanup(func() { config.Store(original) })
	first := *original
	first.GeminiKey = "first-key"
	second := first
	second.GeminiKey = "second-key"
	config.Store(&first)

	linked := NewPtrLinkedCfg(
		func(old, new *Config) bool { return old.GeminiKey != new.GeminiKey },
		func(new *Config) *string {
			value := new.GeminiKey
			return &value
		},
	)

	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(5)
	go func() {
		defer wg.Done()
		for i := range iterations {
			if i%2 == 0 {
				config.Store(&first)
			} else {
				config.Store(&second)
			}
		}
	}()
	for range 4 {
		go func() {
			defer wg.Done()
			for range iterations {
				value := *linked.Get()
				if value != first.GeminiKey && value != second.GeminiKey {
					t.Errorf("linked value = %q", value)
					return
				}
			}
		}()
	}
	wg.Wait()
}
