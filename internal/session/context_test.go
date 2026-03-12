package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAndRead(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	if err := store.Write("telegram", "123456789"); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	ctx, err := store.Read()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
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
}

func TestReadNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	_, err := store.Read()
	if err == nil {
		t.Fatal("expected error when file not found")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected file not found error, got: %v", err)
	}
}

func TestWriteAtomicity(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	for i := 0; i < 10; i++ {
		if err := store.Write("telegram", fmt.Sprintf("chat-%d", i)); err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}
	}

	ctx, err := store.Read()
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if ctx.ChatID != "chat-9" {
		t.Errorf("expected last chatID 'chat-9', got '%s'", ctx.ChatID)
	}

	files, _ := os.ReadDir(tmpDir)
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".tmp" {
			t.Errorf("found temp file: %s", f.Name())
		}
	}
}

func TestWriteConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	store := NewStore(tmpDir)

	const numGoroutines = 20
	done := make(chan int, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			if err := store.Write("telegram", fmt.Sprintf("chat-%d", id)); err != nil {
				t.Errorf("concurrent write %d failed: %v", id, err)
			}
			done <- id
		}(i)
	}

	completed := make(map[int]bool)
	for i := 0; i < numGoroutines; i++ {
		id := <-done
		completed[id] = true
	}

	if len(completed) != numGoroutines {
		t.Errorf("expected %d completed writes, got %d", numGoroutines, len(completed))
	}

	ctx, err := store.Read()
	if err != nil {
		t.Fatalf("Read after concurrent writes failed: %v", err)
	}
	if ctx.Channel != "telegram" {
		t.Errorf("expected channel 'telegram', got '%s'", ctx.Channel)
	}

	files, _ := os.ReadDir(tmpDir)
	for _, f := range files {
		if filepath.Ext(f.Name()) == ".tmp" {
			t.Errorf("found temp file after concurrent writes: %s", f.Name())
		}
	}
}
