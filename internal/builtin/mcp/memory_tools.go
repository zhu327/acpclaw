package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/zhu327/acpclaw/internal/domain"
)

var knowledgeTopics = map[string]string{
	"owner-profile": "Owner personal info, background, career",
	"preferences":   "Preferences, habits, tools, workflow",
	"people":        "People and contacts",
	"projects":      "Project notes, technical decisions",
	"notes":         "General knowledge and miscellaneous",
}

var knowledgeIDs = func() []string {
	ids := make([]string, 0, len(knowledgeTopics))
	for k := range knowledgeTopics {
		ids = append(ids, k)
	}
	slices.Sort(ids)
	return ids
}()

func memoryReadTool() mcp.Tool {
	return mcp.NewTool(
		"memory_read",
		mcp.WithDescription(
			"Read the full content of a memory entry by id. Use memory_list to discover available ids.",
		),
		mcp.WithString(
			"id",
			mcp.Required(),
			mcp.Description("Memory entry id, e.g. 'SOUL', 'owner-profile', 'preferences'"),
		),
	)
}

func memorySearchTool() mcp.Tool {
	return mcp.NewTool(
		"memory_search",
		mcp.WithDescription(
			"Full-text search across all memories. Returns up to 5 matching entries with content snippets.",
		),
		mcp.WithString(
			"query",
			mcp.Required(),
			mcp.Description("Search keywords or phrase, e.g. 'user name', 'project deadline'"),
		),
		mcp.WithString(
			"category",
			mcp.Description(
				"Optional filter: 'identity' (SOUL), 'episode' (conversations), or 'knowledge' (structured notes)",
			),
		),
	)
}

func memorySaveTool() mcp.Tool {
	return mcp.NewTool(
		"memory_save",
		mcp.WithDescription(
			"Save or overwrite a memory entry. "+
				"Use category='identity' to write the agent's personality (SOUL.md, id is ignored). "+
				"Use category='knowledge' (default) to write structured notes by topic.",
		),
		mcp.WithString("id", mcp.Description("Required for knowledge. One of: "+strings.Join(knowledgeIDs, ", "))),
		mcp.WithString("content", mcp.Required(), mcp.Description("Markdown content to save")),
		mcp.WithString(
			"category",
			mcp.Description("'identity' = agent personality (SOUL.md), 'knowledge' = structured notes (default)"),
		),
	)
}

func memoryListTool() mcp.Tool {
	return mcp.NewTool("memory_list",
		mcp.WithDescription("List all stored memory entries. Returns id, title, category, and date for each entry."),
		mcp.WithString("category", mcp.Description("Optional filter: 'identity', 'episode', or 'knowledge'")),
	)
}

func expandEpisodeTool() mcp.Tool {
	return mcp.NewTool(
		"expand_episode",
		mcp.WithDescription(
			"Retrieve the full raw conversation transcript for a session episode. "+
				"Use this when an episode summary has expand_details in its front matter and you need "+
				"the exact original dialogue (code, errors, commands, etc.).",
		),
		mcp.WithString("episodeId", mcp.Required(),
			mcp.Description("Episode ID from memory_list or memory_search, e.g. '2026-03-19-143022-a1b2'")),
	)
}

// rawReference holds parsed offset metadata from an episode's "> Raw Reference:" lines.
type rawReference struct {
	ChatKey string
	Date    string
	Start   int64
	End     int64
}

func expandEpisodeHandler(
	store MemoryStore,
	history HistoryReader,
) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		episodeID := mcp.ParseString(req, "episodeId", "")
		if episodeID == "" {
			return mcp.NewToolResultError("episodeId is required"), nil
		}

		episode, err := store.Read(episodeID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("read episode: %v", err)), nil
		}
		if episode == nil {
			return mcp.NewToolResultError(fmt.Sprintf("episode %q not found", episodeID)), nil
		}

		refs := parseRawReferences(episode.Content)
		if len(refs) == 0 {
			return mcp.NewToolResultText("No raw transcript references found in this episode."), nil
		}
		parts := make([]string, len(refs))
		for i, ref := range refs {
			parts[i] = readRawChunkOrPlaceholder(history, ref)
		}
		return mcp.NewToolResultText(strings.Join(parts, "\n\n---\n\n")), nil
	}
}

func readRawChunkOrPlaceholder(history HistoryReader, ref rawReference) string {
	chunk, err := history.ReadRawHistory(ref.ChatKey, ref.Date, ref.Start, ref.End)
	if err != nil {
		slog.Warn("expand_episode: failed to read raw history", "date", ref.Date, "error", err)
		return fmt.Sprintf("[error reading %s]", ref.Date)
	}
	return chunk
}

func parseRawReferences(content string) []rawReference {
	var refs []rawReference
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "> Raw Reference:") {
			continue
		}
		var ref rawReference
		for _, pair := range strings.Split(strings.TrimPrefix(line, "> Raw Reference:"), ",") {
			k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok {
				continue
			}
			applyRefField(&ref, strings.TrimSpace(k), strings.TrimSpace(v))
		}
		if ref.ChatKey != "" && ref.Date != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func applyRefField(ref *rawReference, k, v string) {
	switch k {
	case "chat_key":
		ref.ChatKey = v
	case "date":
		ref.Date = v
	case "start_offset":
		_, _ = fmt.Sscanf(v, "%d", &ref.Start)
	case "end_offset":
		_, _ = fmt.Sscanf(v, "%d", &ref.End)
	}
}

func memoryReadHandler(svc MemoryStore) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		entry, err := svc.Read(id)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("read error: %v", err)), nil
		}
		if entry == nil {
			return mcp.NewToolResultError(fmt.Sprintf("no memory found with id %q", id)), nil
		}
		return mcp.NewToolResultText(formatEntry(entry)), nil
	}
}

func memorySearchHandler(svc MemoryStore) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query := mcp.ParseString(req, "query", "")
		if query == "" {
			return mcp.NewToolResultError("query is required"), nil
		}
		category := mcp.ParseString(req, "category", "")
		results, err := svc.Search(query, category)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search error: %v", err)), nil
		}
		if len(results) == 0 {
			return mcp.NewToolResultText("No matching memories found."), nil
		}
		parts := make([]string, len(results))
		for i := range results {
			parts[i] = formatEntry(&results[i])
		}
		return mcp.NewToolResultText(strings.Join(parts, "\n\n---\n\n")), nil
	}
}

func memorySaveHandler(svc MemoryStore) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		content := mcp.ParseString(req, "content", "")
		if content == "" {
			return mcp.NewToolResultError("content is required"), nil
		}
		category := mcp.ParseString(req, "category", "knowledge")

		if errRes := validateSaveParams(id, category); errRes != nil {
			return errRes, nil
		}

		entryID, title := resolveEntryIDAndTitle(id, category)
		entry := domain.MemoryEntry{ID: entryID, Category: category, Title: title, Content: content}
		if err := svc.Save(entry); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("save error: %v", err)), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Saved: id=%q category=%q", entryID, category)), nil
	}
}

func validateSaveParams(id, category string) *mcp.CallToolResult {
	if category != "knowledge" {
		return nil
	}
	if id == "" {
		return mcp.NewToolResultError("id is required for knowledge")
	}
	if _, ok := knowledgeTopics[id]; !ok {
		return mcp.NewToolResultError(
			fmt.Sprintf("invalid id %q, must be one of: %s", id, strings.Join(knowledgeIDs, ", ")),
		)
	}
	return nil
}

func resolveEntryIDAndTitle(id, category string) (entryID, title string) {
	if category == "identity" {
		return "SOUL", "Soul — Personality & Values"
	}
	if t, ok := knowledgeTopics[id]; ok {
		return id, t
	}
	return id, id
}

func memoryListHandler(svc MemoryStore) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		category := mcp.ParseString(req, "category", "")
		items, err := svc.List(category)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("list error: %v", err)), nil
		}
		if len(items) == 0 {
			return mcp.NewToolResultText("No memories stored yet."), nil
		}
		lines := make([]string, len(items))
		for i, item := range items {
			lines[i] = formatListLine(&item)
		}
		return mcp.NewToolResultText(strings.Join(lines, "\n")), nil
	}
}

func formatListLine(item *domain.MemoryEntry) string {
	base := fmt.Sprintf("- id: %s | title: %s | category: %s | date: %s", item.ID, item.Title, item.Category, item.Date)
	if len(item.Tags) > 0 {
		return base + "  tags: " + strings.Join(item.Tags, ", ")
	}
	return base
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
