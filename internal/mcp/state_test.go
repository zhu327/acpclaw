package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestState_WriteAndRead(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	state := &ChannelState{
		Sessions:      map[string]int64{"sess-1": 42},
		LastSessionID: "sess-1",
	}
	require.NoError(t, store.Write(state))
	got, err := store.Read()
	require.NoError(t, err)
	assert.Equal(t, int64(42), got.Sessions["sess-1"])
	assert.Equal(t, "sess-1", got.LastSessionID)
}

func TestState_FileMode(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, store.Write(&ChannelState{Sessions: map[string]int64{}}))
	info, err := os.Stat(store.path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestState_AddAndRemoveSession(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, store.Write(&ChannelState{Sessions: map[string]int64{}}))
	require.NoError(t, store.AddSession("sess-1", 42))
	require.NoError(t, store.AddSession("sess-2", 99))
	state, err := store.Read()
	require.NoError(t, err)
	assert.Equal(t, int64(42), state.Sessions["sess-1"])
	assert.Equal(t, int64(99), state.Sessions["sess-2"])
	assert.Equal(t, "sess-2", state.LastSessionID) // last added
	require.NoError(t, store.RemoveSession("sess-1"))
	state, err = store.Read()
	require.NoError(t, err)
	_, ok := state.Sessions["sess-1"]
	assert.False(t, ok)
}

func TestState_AddSession_ReplacesPreviousSessionForSameChat(t *testing.T) {
	store := NewStateStore(filepath.Join(t.TempDir(), "state.json"))
	require.NoError(t, store.Write(&ChannelState{Sessions: map[string]int64{}}))

	require.NoError(t, store.AddSession("sess-old", 42))
	require.NoError(t, store.AddSession("sess-new", 42))

	state, err := store.Read()
	require.NoError(t, err)
	assert.Equal(t, int64(42), state.Sessions["sess-new"])
	_, hasOld := state.Sessions["sess-old"]
	assert.False(t, hasOld, "previous session for same chat should be removed")
	assert.Equal(t, "sess-new", state.LastSessionID)
}

func TestState_ResolveChatID_Single(t *testing.T) {
	state := &ChannelState{Sessions: map[string]int64{"s": 42}, LastSessionID: "s"}
	chatID, err := ResolveChatID(state, "")
	require.NoError(t, err)
	assert.Equal(t, int64(42), chatID)
}

func TestState_ResolveChatID_ByID(t *testing.T) {
	state := &ChannelState{Sessions: map[string]int64{"s1": 42, "s2": 99}}
	chatID, err := ResolveChatID(state, "s1")
	require.NoError(t, err)
	assert.Equal(t, int64(42), chatID)
}

func TestState_ResolveChatID_RequiredWhenMultiple(t *testing.T) {
	state := &ChannelState{Sessions: map[string]int64{"s1": 42, "s2": 99}}
	_, err := ResolveChatID(state, "")
	assert.Error(t, err)
}
