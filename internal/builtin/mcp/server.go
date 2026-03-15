package mcp

import (
	"github.com/mark3labs/mcp-go/server"
	"github.com/zhu327/acpclaw/internal/domain"
)

// MemoryStore defines the interface required by MCP memory tools.
type MemoryStore interface {
	Read(id string) (*domain.MemoryEntry, error)
	Search(query, category string) ([]domain.MemoryEntry, error)
	Save(entry domain.MemoryEntry) error
	List(category string) ([]domain.MemoryEntry, error)
}

// CronStore defines the interface required by MCP cron tools.
type CronStore interface {
	AddJob(job domain.CronJob) error
	LoadJobs(channel, chatID string) ([]domain.CronJob, error)
	DeleteJob(channel, chatID, jobID string) error
	ListAllJobs() ([]domain.CronJob, error)
}

// NewServerWithMemoryAndCron creates an MCP server with memory and cron tools.
func NewServerWithMemoryAndCron(memoryStore MemoryStore, cronStore CronStore) *server.MCPServer {
	s := server.NewMCPServer("acpclaw", "1.0.0")

	if memoryStore != nil {
		s.AddTool(memoryReadTool(), memoryReadHandler(memoryStore))
		s.AddTool(memorySearchTool(), memorySearchHandler(memoryStore))
		s.AddTool(memorySaveTool(), memorySaveHandler(memoryStore))
		s.AddTool(memoryListTool(), memoryListHandler(memoryStore))
	}

	if cronStore != nil {
		s.AddTool(cronCreateTool(), cronCreateHandler(cronStore))
		s.AddTool(cronListTool(), cronListHandler(cronStore))
		s.AddTool(cronDeleteTool(), cronDeleteHandler(cronStore))
	}
	return s
}
