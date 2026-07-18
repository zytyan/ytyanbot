package handlers

import (
	"fmt"
	"main/helpers/cocdice"
	"sync"
	"testing"
)

func TestBattleRegistryAndSessionAreConcurrentSafe(t *testing.T) {
	registry := newBattleRegistry()
	session := &battleSession{groupID: -1001, round: cocdice.NewFromText("Alice\nBob")}
	if !registry.add("battle-1", session) {
		t.Fatal("first battle registration failed")
	}
	if registry.add("battle-2", &battleSession{groupID: -1001, round: cocdice.NewFromText("Eve")}) {
		t.Fatal("duplicate group battle registration succeeded")
	}

	const workers = 100
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			temporaryID := fmt.Sprintf("temporary-%d", index)
			temporaryGroup := int64(-2000 - index)
			if !registry.add(temporaryID, &battleSession{
				groupID: temporaryGroup,
				round:   cocdice.NewFromText("Temporary"),
			}) {
				t.Errorf("temporary battle registration failed")
				return
			}
			if !registry.remove(temporaryID, temporaryGroup) {
				t.Errorf("temporary battle removal failed")
				return
			}
			stored, ok := registry.byUID("battle-1", -1001)
			if !ok {
				t.Errorf("battle lookup failed")
				return
			}
			if index%2 == 0 {
				stored.next()
				return
			}
			stored.execute([]string{fmt.Sprintf("stat Alice status-%d", index)})
		}(i)
	}
	wg.Wait()

	uid, stored, ok := registry.forGroup(-1001)
	if !ok || uid != "battle-1" || stored != session {
		t.Fatalf("group lookup = (%q, %p, %v), want registered session", uid, stored, ok)
	}
	if !registry.remove("battle-1", -1001) {
		t.Fatal("battle removal failed")
	}
	if registry.hasGroup(-1001) {
		t.Fatal("group mapping remained after removal")
	}
}
