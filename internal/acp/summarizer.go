package acp

import (
	"context"
	"fmt"
	"time"
)

const summarizeDateFormat = "2006-01-02"

// AgentSummarizer 使用 AgentService 生成会话摘要
type AgentSummarizer struct {
	agentSvc AgentService
	chatID   int64
}

// NewAgentSummarizer 创建基于 Agent 的摘要生成器
func NewAgentSummarizer(agentSvc AgentService, chatID int64) *AgentSummarizer {
	return &AgentSummarizer{
		agentSvc: agentSvc,
		chatID:   chatID,
	}
}

// Summarize 生成会话摘要
func (s *AgentSummarizer) Summarize(ctx context.Context, transcript string) (string, error) {
	prompt := buildSummarizePrompt(transcript)
	reply, err := s.agentSvc.Prompt(ctx, s.chatID, PromptInput{Text: prompt})
	if err != nil {
		return "", fmt.Errorf("summarize prompt: %w", err)
	}
	if reply == nil {
		return "", nil
	}
	return reply.Text, nil
}

func buildSummarizePrompt(transcript string) string {
	return fmt.Sprintf(`请将以下对话总结为结构化摘要，直接输出 Markdown 格式如下:
---
title: "<简洁标题>"
date: %s
tags: [<相关标签>]
---

## Summary
<2-4 句话概述>

## Key Topics
- <主题 1>
- <主题 2>

## Decisions & Outcomes
- <决定或结果>

## Notable Information
- <值得记住的信息>

对话内容:
%s`, time.Now().Format(summarizeDateFormat), transcript)
}
