package agent

import (
	"context"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/zhu327/acpclaw/internal/domain"
)

type listSessionsRequest struct {
	Cursor *string `json:"cursor,omitempty"`
	Cwd    *string `json:"cwd,omitempty"`
}

type listSessionsResponse struct {
	Sessions   []sessionListItem `json:"sessions"`
	NextCursor *string           `json:"nextCursor,omitempty"`
}

type sessionListItem struct {
	SessionID string  `json:"sessionId"`
	Cwd       string  `json:"cwd"`
	Title     *string `json:"title,omitempty"`
	UpdatedAt *string `json:"updatedAt,omitempty"`
}

func sessionItemToSessionInfo(item sessionListItem) domain.SessionInfo {
	info := domain.SessionInfo{SessionID: item.SessionID, Workspace: item.Cwd}
	if item.Title != nil {
		info.Title = *item.Title
	}
	if item.UpdatedAt != nil {
		if t, err := time.Parse(time.RFC3339, *item.UpdatedAt); err == nil {
			info.UpdatedAt = t
		}
	}
	return info
}

func callSessionList(
	ctx context.Context,
	conn *acpsdk.Connection,
	cwd string,
	timeout time.Duration,
) ([]sessionListItem, error) {
	var sessions []sessionListItem
	var cursor *string
	for range 5 { // max 5 pages
		req := listSessionsRequest{Cursor: cursor}
		if cwd != "" {
			req.Cwd = &cwd
		}
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		resp, err := acpsdk.SendRequest[listSessionsResponse](conn, callCtx, "session/list", req)
		cancel()
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, resp.Sessions...)
		if resp.NextCursor == nil || *resp.NextCursor == "" {
			break
		}
		cursor = resp.NextCursor
	}
	return sessions, nil
}
