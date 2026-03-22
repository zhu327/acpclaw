package builtin

import (
	"time"

	"github.com/zhu327/acpclaw/internal/config"
)

// idleReclaimDuration maps normalized prompt_queue config to the manager's idle timeout (0 = never reclaim).
func idleReclaimDuration(pq config.PromptQueueConfig) time.Duration {
	if pq.IdleTimeoutSeconds == config.PromptQueueIdleReclaimDisabled {
		return 0
	}
	return time.Duration(pq.IdleTimeoutSeconds) * time.Second
}
