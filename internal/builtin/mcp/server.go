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

// HistoryReader provides byte-range access to raw conversation history.
type HistoryReader interface {
	ReadRawHistory(chatKey, date string, start, end int64) (string, error)
}

// CronStore defines the interface required by MCP cron tools.
type CronStore interface {
	AddJob(job domain.CronJob) error
	LoadJobs(channel, chatID string) ([]domain.CronJob, error)
	DeleteJob(channel, chatID, jobID string) error
	ListAllJobs() ([]domain.CronJob, error)
}

// NewServer creates an MCP server with memory, history, and cron tools.
func NewServer(memoryStore MemoryStore, historyReader HistoryReader, cronStore CronStore) *server.MCPServer {
	s := server.NewMCPServer("acpclaw", "1.0.0")

	if memoryStore != nil {
		s.AddTool(memoryReadTool(), memoryReadHandler(memoryStore))
		s.AddTool(memorySearchTool(), memorySearchHandler(memoryStore))
		s.AddTool(memorySaveTool(), memorySaveHandler(memoryStore))
		s.AddTool(memoryListTool(), memoryListHandler(memoryStore))

		if historyReader != nil {
			s.AddTool(expandEpisodeTool(), expandEpisodeHandler(memoryStore, historyReader))
		}
	}

	if cronStore != nil {
		s.AddTool(cronCreateTool(), cronCreateHandler(cronStore))
		s.AddTool(cronListTool(), cronListHandler(cronStore))
		s.AddTool(cronDeleteTool(), cronDeleteHandler(cronStore))
	}
	return s
}
