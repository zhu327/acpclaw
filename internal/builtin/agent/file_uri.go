package agent

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhu327/acpclaw/internal/domain"
)

// fileURIResult is the typed result of resolveFileURI.
type fileURIResult struct {
	data        []byte
	name        string
	warning     string
	passThrough bool // true when the file should be forwarded as-is (non-local URI)
}

func extractFileURI(f domain.FileData) string {
	if strings.HasPrefix(f.Name, "file://") {
		return f.Name
	}
	if strings.HasPrefix(string(f.Data), "file://") {
		return strings.TrimSpace(string(f.Data))
	}
	return ""
}

func fileURIWarning(format string, args ...any) fileURIResult {
	return fileURIResult{warning: "Attachment warning: " + fmt.Sprintf(format, args...) + "\n"}
}

func resolveFileURI(f domain.FileData, workspaceAbs string) fileURIResult {
	fileURI := extractFileURI(f)
	if fileURI == "" {
		return fileURIResult{}
	}
	u, err := url.Parse(fileURI)
	if err != nil {
		return fileURIWarning("%s: invalid URI", fileURI)
	}
	if u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") {
		return fileURIResult{passThrough: true}
	}
	path, err := url.PathUnescape(u.Path)
	if err != nil {
		return fileURIWarning("%s: invalid path encoding", fileURI)
	}
	if path == "" {
		return fileURIWarning("%s: empty path after decode", fileURI)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fileURIWarning("%s: %v", filepath.Base(path), err)
	}
	rel, err := filepath.Rel(workspaceAbs, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return fileURIWarning("%s: path outside workspace", filepath.Base(path))
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return fileURIWarning("%s: %v", filepath.Base(path), err)
	}
	if info.IsDir() {
		return fileURIWarning("%s: path is a directory, not a file", filepath.Base(path))
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return fileURIWarning("%s: %v", filepath.Base(path), err)
	}
	return fileURIResult{data: data, name: filepath.Base(resolved)}
}

// ResolveFileURIResources resolves file:// URIs in reply files to actual content.
func (s *AcpAgentService) ResolveFileURIResources(reply *domain.AgentReply, workspace string) *domain.AgentReply {
	if reply == nil {
		return nil
	}
	out := &domain.AgentReply{
		Text:       reply.Text,
		Images:     append([]domain.ImageData(nil), reply.Images...),
		Files:      nil,
		Activities: append([]domain.ActivityBlock(nil), reply.Activities...),
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		workspaceAbs = filepath.Clean(workspace)
	}
	if evaluatedAbs, err := filepath.EvalSymlinks(workspaceAbs); err == nil {
		workspaceAbs = evaluatedAbs
	}
	for _, f := range reply.Files {
		r := resolveFileURI(f, workspaceAbs)
		switch {
		case r.passThrough:
			out.Files = append(out.Files, f)
		case r.warning != "":
			out.Text += r.warning
		case r.data == nil:
			out.Files = append(out.Files, f)
		case strings.HasPrefix(f.MIMEType, "image/"):
			out.Images = append(out.Images, domain.ImageData{MIMEType: f.MIMEType, Data: r.data, Name: r.name})
		default:
			out.Files = append(out.Files, domain.FileData{MIMEType: f.MIMEType, Data: r.data, Name: r.name})
		}
	}
	return out
}
