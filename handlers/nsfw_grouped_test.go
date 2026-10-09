package handlers

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
)

func waitGroupedDetectorCleanup(t *testing.T, key groupedMsgK) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, exists := groupedDetectMap.Load(key); !exists {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("group detector %+v was not cleaned up", key)
}

func TestProcessGroupedNsfwChecksEveryMessageButRepliesOnce(t *testing.T) {
	key := groupedMsgK{ChatId: -940001, GroupId: "first-message"}
	first := &gotgbot.Message{MessageId: 1}
	second := &gotgbot.Message{MessageId: 2}
	var detected []int64
	var replies int
	detect := func(msg *gotgbot.Message, allowReply bool) bool {
		detected = append(detected, msg.MessageId)
		if allowReply {
			replies++
		}
		return allowReply
	}
	processGroupedNsfw(key, first, 10*time.Millisecond, detect)
	processGroupedNsfw(key, second, 10*time.Millisecond, detect)
	if len(detected) != 2 || detected[0] != first.MessageId || detected[1] != second.MessageId || replies != 1 {
		t.Fatalf("detected messages = %v, replies = %d; want both messages and one reply", detected, replies)
	}
	waitGroupedDetectorCleanup(t, key)
}

func TestProcessGroupedNsfwSerializesConcurrentMessages(t *testing.T) {
	key := groupedMsgK{ChatId: -940002, GroupId: "concurrent"}
	const messages = 100
	var detected atomic.Int32
	var wg sync.WaitGroup
	for id := range messages {
		wg.Add(1)
		go func() {
			defer wg.Done()
			processGroupedNsfw(key, &gotgbot.Message{MessageId: int64(id)}, 20*time.Millisecond,
				func(*gotgbot.Message, bool) bool {
					detected.Add(1)
					return false
				})
		}()
	}
	wg.Wait()
	if got := detected.Load(); got != messages {
		t.Fatalf("detected messages = %d, want %d", got, messages)
	}
	waitGroupedDetectorCleanup(t, key)
}
