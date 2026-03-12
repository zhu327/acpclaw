package acp

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/zhu327/acpclaw/internal/session"
)

func withTestChatContextDir(t *testing.T, fn func(dir string)) {
	tmpDir := t.TempDir()
	orig := defaultStore
	defaultStore = session.NewStore(tmpDir)
	defer func() { defaultStore = orig }()
	fn(tmpDir)
}

func TestWriteAndReadChatContext(t *testing.T) {
	withTestChatContextDir(t, func(_ string) {
		if err := WriteChatContext("telegram", "123456789"); err != nil {
			t.Fatalf("WriteChatContext failed: %v", err)
		}

		ctx, err := ReadChatContext()
		if err != nil {
			t.Fatalf("ReadChatContext failed: %v", err)
		}

		if ctx.Channel != "telegram" {
			t.Errorf("expected channel 'telegram', got '%s'", ctx.Channel)
		}
		if ctx.ChatID != "123456789" {
			t.Errorf("expected chatID '123456789', got '%s'", ctx.ChatID)
		}
		if ctx.UpdatedAt.IsZero() {
			t.Error("expected non-zero UpdatedAt")
		}
	})
}

func TestReadChatContextNotFound(t *testing.T) {
	withTestChatContextDir(t, func(_ string) {
		_, err := ReadChatContext()
		if err == nil {
			t.Fatal("expected error when file not found")
		}
		if !os.IsNotExist(err) {
			t.Errorf("expected file not found error, got: %v", err)
		}
	})
}

func TestWriteChatContextAtomicity(t *testing.T) {
	withTestChatContextDir(t, func(dir string) {
		for i := 0; i < 10; i++ {
			if err := WriteChatContext("telegram", fmt.Sprintf("chat-%d", i)); err != nil {
				t.Fatalf("write %d failed: %v", i, err)
			}
		}

		ctx, err := ReadChatContext()
		if err != nil {
			t.Fatalf("ReadChatContext failed: %v", err)
		}
		if ctx.ChatID != "chat-9" {
			t.Errorf("expected last chatID 'chat-9', got '%s'", ctx.ChatID)
		}

		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if filepath.Ext(f.Name()) == ".tmp" {
				t.Errorf("found temp file: %s", f.Name())
			}
		}
	})
}

func TestWriteChatContextConcurrency(t *testing.T) {
	withTestChatContextDir(t, func(dir string) {
		const numGoroutines = 20
		done := make(chan int, numGoroutines)

		// Launch multiple concurrent writers
		for i := 0; i < numGoroutines; i++ {
			go func(id int) {
				if err := WriteChatContext("telegram", fmt.Sprintf("chat-%d", id)); err != nil {
					t.Errorf("concurrent write %d failed: %v", id, err)
				}
				done <- id
			}(i)
		}

		// Wait for all to complete
		completed := make(map[int]bool)
		for i := 0; i < numGoroutines; i++ {
			id := <-done
			completed[id] = true
		}

		// Verify all completed
		if len(completed) != numGoroutines {
			t.Errorf("expected %d completed writes, got %d", numGoroutines, len(completed))
		}

		// Verify final context is valid (should be one of the written values)
		ctx, err := ReadChatContext()
		if err != nil {
			t.Fatalf("ReadChatContext after concurrent writes failed: %v", err)
		}
		if ctx.Channel != "telegram" {
			t.Errorf("expected channel 'telegram', got '%s'", ctx.Channel)
		}

		// Verify no temp files left behind
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if filepath.Ext(f.Name()) == ".tmp" {
				t.Errorf("found temp file after concurrent writes: %s", f.Name())
			}
		}
	})
}
