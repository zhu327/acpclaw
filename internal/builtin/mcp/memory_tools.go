package mcp

import (
	"context"
	"fmt"
	"strings"

	mcplib "github.com/mark3labs/mcp-go/mcp"
	"github.com/zhu327/acpclaw/internal/domain"
)

var knowledgeTopics = map[string]string{
	"owner-profile": "Owner personal info, background, career",
	"preferences":   "Preferences, habits, tools, workflow",
	"people":        "People and contacts",
	"projects":      "Project notes, technical decisions",
	"notes":         "General knowledge and miscellaneous",
}

func memoryReadTool() mcplib.Tool {
	return mcplib.NewTool("memory_read",
		mcplib.WithDescription("Read the full content of a memory entry by id."),
		mcplib.WithString("id", mcplib.Required(), mcplib.Description("Memory entry id")),
	)
}

func memorySearchTool() mcplib.Tool {
	return mcplib.NewTool("memory_search",
		mcplib.WithDescription("Full-text search across all memories. Returns up to 5 matches."),
		mcplib.WithString("query", mcplib.Required(), mcplib.Description("Search query text")),
		mcplib.WithString("category", mcplib.Description("Filter: identity, episode, knowledge")),
	)
}

func memorySaveTool() mcplib.Tool {
	topicKeys := make([]string, 0, len(knowledgeTopics))
	for k := range knowledgeTopics {
		topicKeys = append(topicKeys, k)
	}
	return mcplib.NewTool("memory_save",
		mcplib.WithDescription(
			"Save or overwrite a memory entry. For identity: writes SOUL.md. For knowledge: writes knowledge/{id}.md.",
		),
		mcplib.WithString("id", mcplib.Description("Memory id, one of: "+strings.Join(topicKeys, ", "))),
		mcplib.WithString("content", mcplib.Required(), mcplib.Description("Markdown content to save")),
		mcplib.WithString("category", mcplib.Description("Target: identity or knowledge (default)")),
	)
}

func memoryListTool() mcplib.Tool {
	return mcplib.NewTool("memory_list",
		mcplib.WithDescription("List all stored memory entries with id, title, category, date."),
		mcplib.WithString("category", mcplib.Description("Filter: identity, episode, knowledge")),
	)
}

func memoryReadHandler(
	svc MemoryStore,
) func(context.Context, mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return func(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		id, _ := mcplib.ParseArgument(req, "id", "").(string)
		if id == "" {
			return mcplib.NewToolResultError("id is required"), nil
		}
		entry, err := svc.Read(id)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("read error: %v", err)), nil
		}
		if entry == nil {
			return mcplib.NewToolResultError(fmt.Sprintf("no memory found with id %q", id)), nil
		}
		return mcplib.NewToolResultText(formatEntry(entry)), nil
	}
}

func memorySearchHandler(
	svc MemoryStore,
) func(context.Context, mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return func(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		query, _ := mcplib.ParseArgument(req, "query", "").(string)
		category, _ := mcplib.ParseArgument(req, "category", "").(string)
		if query == "" {
			return mcplib.NewToolResultError("query is required"), nil
		}
		results, err := svc.Search(query, category)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("search error: %v", err)), nil
		}
		if len(results) == 0 {
			return mcplib.NewToolResultText("No matching memories found."), nil
		}
		var parts []string
		for _, r := range results {
			parts = append(parts, formatEntry(&r))
		}
		return mcplib.NewToolResultText(strings.Join(parts, "\n\n---\n\n")), nil
	}
}

func memorySaveHandler(
	svc MemoryStore,
) func(context.Context, mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return func(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		id, _ := mcplib.ParseArgument(req, "id", "").(string)
		content, _ := mcplib.ParseArgument(req, "content", "").(string)
		category, _ := mcplib.ParseArgument(req, "category", "").(string)
		if content == "" {
			return mcplib.NewToolResultError("content is required"), nil
		}
		if category == "" {
			category = "knowledge"
		}
		if category == "knowledge" {
			if id == "" {
				return mcplib.NewToolResultError("id is required for knowledge"), nil
			}
			if _, ok := knowledgeTopics[id]; !ok {
				keys := make([]string, 0, len(knowledgeTopics))
				for k := range knowledgeTopics {
					keys = append(keys, k)
				}
				return mcplib.NewToolResultError(
					fmt.Sprintf("invalid id %q, must be one of: %s", id, strings.Join(keys, ", ")),
				), nil
			}
		}
		title := id
		if category == "identity" {
			id = "SOUL"
			title = "Soul — Personality & Values"
		} else if t, ok := knowledgeTopics[id]; ok {
			title = t
		}
		entry := domain.MemoryEntry{
			ID:       id,
			Category: category,
			Title:    title,
			Content:  content,
		}
		if err := svc.Save(entry); err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("save error: %v", err)), nil
		}
		return mcplib.NewToolResultText(fmt.Sprintf("Saved: id=%q category=%q", id, category)), nil
	}
}

func memoryListHandler(
	svc MemoryStore,
) func(context.Context, mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
	return func(_ context.Context, req mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		category, _ := mcplib.ParseArgument(req, "category", "").(string)
		items, err := svc.List(category)
		if err != nil {
			return mcplib.NewToolResultError(fmt.Sprintf("list error: %v", err)), nil
		}
		if len(items) == 0 {
			return mcplib.NewToolResultText("No memories stored yet."), nil
		}
		var lines []string
		for _, item := range items {
			tags := ""
			if len(item.Tags) > 0 {
				tags = "  tags: " + strings.Join(item.Tags, ", ")
			}
			lines = append(lines, fmt.Sprintf("- id: %s | title: %s | category: %s | date: %s%s",
				item.ID, item.Title, item.Category, item.Date, tags))
		}
		return mcplib.NewToolResultText(strings.Join(lines, "\n")), nil
	}
}

func formatEntry(e *domain.MemoryEntry) string {
	lines := []string{
		fmt.Sprintf("id: %s", e.ID),
		fmt.Sprintf("title: %s", e.Title),
		fmt.Sprintf("category: %s", e.Category),
		fmt.Sprintf("date: %s", e.Date),
	}
	if len(e.Tags) > 0 {
		lines = append(lines, fmt.Sprintf("tags: %s", strings.Join(e.Tags, ", ")))
	}
	lines = append(lines, "", e.Content)
	return strings.Join(lines, "\n")
}
