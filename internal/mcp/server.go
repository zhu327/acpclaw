package mcp

import (
	"github.com/mark3labs/mcp-go/server"
	"github.com/zhu327/acpclaw/internal/cron"
	"github.com/zhu327/acpclaw/internal/memory"
	"github.com/zhu327/acpclaw/internal/session"
)

// MemoryStore defines the interface required by MCP memory tools.
type MemoryStore interface {
	Read(id string) (*memory.MemoryEntry, error)
	Search(query, category string) ([]memory.MemoryEntry, error)
	Save(entry memory.MemoryEntry) error
	List(category string) ([]memory.MemoryEntry, error)
}

// CronStore defines the interface required by MCP cron tools.
type CronStore interface {
	AddJob(job cron.Job) error
	LoadJobs(channel, chatID string) ([]cron.Job, error)
	DeleteJob(channel, chatID, jobID string) error
	ListAllJobs() ([]cron.Job, error)
}

// SessionContextStore defines the interface required by MCP tools to know the active chat context.
type SessionContextStore interface {
	Read() (*session.Context, error)
}

// NewServer creates a minimal MCP server with no tools.
func NewServer() *server.MCPServer {
	return NewServerWithMemoryAndCron(nil, nil, nil)
}

// NewServerWithMemoryAndCron creates an MCP server with memory and cron tools.
func NewServerWithMemoryAndCron(
	memoryStore MemoryStore,
	cronStore CronStore,
	sessionStore SessionContextStore,
) *server.MCPServer {
	s := server.NewMCPServer("acpclaw", "1.0.0")

	if memoryStore != nil {
		s.AddTool(memoryReadTool(), memoryReadHandler(memoryStore))
		s.AddTool(memorySearchTool(), memorySearchHandler(memoryStore))
		s.AddTool(memorySaveTool(), memorySaveHandler(memoryStore))
		s.AddTool(memoryListTool(), memoryListHandler(memoryStore))
	}

	if cronStore != nil && sessionStore != nil {
		s.AddTool(cronCreateTool(), cronCreateHandler(cronStore, sessionStore))
		s.AddTool(cronListTool(), cronListHandler(cronStore, sessionStore))
		s.AddTool(cronDeleteTool(), cronDeleteHandler(cronStore, sessionStore))
	}
	return s
}
