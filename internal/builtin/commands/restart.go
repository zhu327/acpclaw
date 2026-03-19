package commands

import (
	"context"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/zhu327/acpclaw/internal/domain"
)

// restartDelay 延迟执行 restart，确保响应先发送回客户端
const restartDelay = time.Second

// RestartCommand handles /restart — restarts the acpclaw process in-place.
type RestartCommand struct {
	shutdownFn func()
}

// NewRestartCommand creates a RestartCommand.
func NewRestartCommand(shutdownFn func()) *RestartCommand {
	return &RestartCommand{shutdownFn: shutdownFn}
}

func (c *RestartCommand) Name() string        { return "restart" }
func (c *RestartCommand) Description() string { return "Restart acpclaw process" }

func (c *RestartCommand) Execute(ctx context.Context, args []string, tc *domain.TurnContext) (*domain.Result, error) {
	exe, err := os.Executable()
	if err != nil {
		return &domain.Result{Text: "❌ Failed to resolve executable path."}, nil
	}

	go c.delayedRestart(exe)
	return &domain.Result{Text: "♻️ Restarting acpclaw…"}, nil
}

func (c *RestartCommand) delayedRestart(exe string) {
	time.Sleep(restartDelay)
	slog.Info("restarting acpclaw process", "exe", exe, "args", os.Args)
	if c.shutdownFn != nil {
		c.shutdownFn()
	}
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		slog.Error("failed to restart process, falling back to SIGTERM", "error", err)
		p, _ := os.FindProcess(os.Getpid())
		_ = p.Signal(syscall.SIGTERM)
	}
}
