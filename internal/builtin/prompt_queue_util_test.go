package builtin

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/zhu327/acpclaw/internal/config"
)

func TestIdleReclaimDuration(t *testing.T) {
	t.Parallel()
	assert.Equal(t, time.Duration(0), idleReclaimDuration(config.PromptQueueConfig{
		IdleTimeoutSeconds: config.PromptQueueIdleReclaimDisabled,
	}))
	assert.Equal(t, 300*time.Second, idleReclaimDuration(config.PromptQueueConfig{
		IdleTimeoutSeconds: 300,
	}))
}
