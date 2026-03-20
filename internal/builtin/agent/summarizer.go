package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/zhu327/acpclaw/internal/domain"
)

const summarizeDateFormat = "2006-01-02"

// AgentSummarizer uses Prompter to generate session summaries.
type AgentSummarizer struct {
	prompter domain.Prompter
}

// NewAgentSummarizer creates an agent-based summarizer.
func NewAgentSummarizer(prompter domain.Prompter) *AgentSummarizer {
	return &AgentSummarizer{prompter: prompter}
}

var _ domain.Summarizer = (*AgentSummarizer)(nil)

// Summarize generates a session summary.
func (s *AgentSummarizer) Summarize(ctx context.Context, chat domain.ChatRef, transcript string) (string, error) {
	prompt := buildSummarizePrompt(transcript)
	reply, err := s.prompter.Prompt(ctx, chat, domain.PromptInput{Text: prompt})
	if err != nil {
		return "", fmt.Errorf("summarize prompt: %w", err)
	}
	if reply == nil {
		return "", nil
	}
	return cleanSummary(reply.Text), nil
}

var codeFenceRe = regexp.MustCompile("(?s)```(?:markdown|md)?\\s*\n(.*?)```")

func cleanSummary(raw string) string {
	if m := codeFenceRe.FindStringSubmatch(raw); len(m) > 1 {
		raw = m[1]
	}
	if idx := strings.Index(raw, "---"); idx > 0 {
		raw = raw[idx:]
	}
	return strings.TrimSpace(raw)
}

func buildSummarizePrompt(transcript string) string {
	const rules = `You are a conversation summarizer. Your ONLY job is to output a structured Markdown summary. Follow these rules strictly:

1. Output ONLY the Markdown content below — no thinking, no explanation, no preamble, no code fences.
2. Start your response with the "---" front matter line. Nothing before it.
3. Keep the summary concise and factual. Use the same language as the conversation.
4. If a section has no relevant content, write "- N/A".`

	const template = `
---
title: "<concise title, 10 words or fewer>"
date: %s
expand_details: "<comma-separated list of dropped specifics, e.g. exact commands, full error output, code snippets>"
---

## Summary
<2-4 sentence overview of the conversation's main content and purpose>

## Key Topics
- <topic 1>
- <topic 2>

## Decisions & Outcomes
- <decisions made or results produced>

<conversation>
%s
</conversation>`
	date := time.Now().Format(summarizeDateFormat)
	return rules + fmt.Sprintf(template, date, transcript)
}
