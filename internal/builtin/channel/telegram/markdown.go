package telegram

import "strings"

const maxChunkUTF16 = 4096

// MessageChunk represents a piece of a message ready to send to Telegram as MarkdownV2.
type MessageChunk struct {
	Text string
}

// UTF16Len returns the length of s in UTF-16 code units.
// Surrogate pairs (e.g. emoji) count as 2.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// mdv2Special lists all characters that must be escaped in MarkdownV2 plain text contexts.
const mdv2Special = `\_*[]()~` + "`" + `>#+-=|{}.!`

// escapeChar returns the MarkdownV2-escaped form of a single rune in plain text context.
func escapeChar(r rune) string {
	if strings.ContainsRune(mdv2Special, r) {
		return `\` + string(r)
	}
	return string(r)
}

// escapeURL escapes only the characters that need escaping inside a MarkdownV2 link URL: ')' and '\'.
func escapeURL(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `)`, `\)`)
	return s
}

// escapeCodeOrPreContent escapes backticks and backslashes inside inline code or pre blocks.
func escapeCodeOrPreContent(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}

// RenderMarkdown converts Markdown text to MarkdownV2 and returns chunks within
// the 4096 UTF-16 code unit limit.
func RenderMarkdown(input string) []MessageChunk {
	if input == "" {
		return nil
	}
	converted := markdownToMDV2(input)
	return splitChunks(converted)
}

// markdownToMDV2 converts a Markdown string to a MarkdownV2-safe string.
// It handles fenced code blocks, inline code, bold, italic, strikethrough,
// links, and escapes all other special characters in plain text regions.
func markdownToMDV2(input string) string {
	var out strings.Builder
	out.Grow(len(input) * 2)

	i := 0
	runes := []rune(input)
	n := len(runes)

	for i < n {
		// Fenced code block: ```[lang]\n...\n```
		if i+2 < n && runes[i] == '`' && runes[i+1] == '`' && runes[i+2] == '`' {
			// Find closing ```
			end := -1
			for j := i + 3; j+2 < n; j++ {
				if runes[j] == '`' && runes[j+1] == '`' && runes[j+2] == '`' {
					end = j
					break
				}
			}
			if end == -1 {
				// No closing fence — treat as plain text
				out.WriteString(escapeChar(runes[i]))
				i++
				continue
			}
			// Extract lang and content
			inner := string(runes[i+3 : end])
			lang := ""
			content := inner
			if nl := strings.IndexByte(inner, '\n'); nl >= 0 {
				lang = strings.TrimSpace(inner[:nl])
				content = inner[nl+1:]
			}
			out.WriteString("```")
			if lang != "" {
				out.WriteString(lang)
			}
			out.WriteString("\n")
			out.WriteString(escapeCodeOrPreContent(content))
			out.WriteString("```")
			i = end + 3
			continue
		}

		// Inline code: `...`
		if runes[i] == '`' {
			end := -1
			for j := i + 1; j < n; j++ {
				if runes[j] == '`' {
					end = j
					break
				}
			}
			if end == -1 {
				out.WriteString(escapeChar(runes[i]))
				i++
				continue
			}
			content := string(runes[i+1 : end])
			out.WriteString("`")
			out.WriteString(escapeCodeOrPreContent(content))
			out.WriteString("`")
			i = end + 1
			continue
		}

		// Bold: **...**
		if i+1 < n && runes[i] == '*' && runes[i+1] == '*' {
			end := -1
			for j := i + 2; j+1 < n; j++ {
				if runes[j] == '*' && runes[j+1] == '*' {
					end = j
					break
				}
			}
			if end == -1 {
				out.WriteString(escapeChar(runes[i]))
				i++
				continue
			}
			inner := markdownToMDV2(string(runes[i+2 : end]))
			out.WriteString("*")
			out.WriteString(inner)
			out.WriteString("*")
			i = end + 2
			continue
		}

		// Italic: *...* (single, not followed by another *)
		if runes[i] == '*' && (i+1 >= n || runes[i+1] != '*') {
			end := -1
			for j := i + 1; j < n; j++ {
				if runes[j] == '*' && (j+1 >= n || runes[j+1] != '*') {
					end = j
					break
				}
			}
			if end == -1 {
				out.WriteString(escapeChar(runes[i]))
				i++
				continue
			}
			inner := markdownToMDV2(string(runes[i+1 : end]))
			out.WriteString("_")
			out.WriteString(inner)
			out.WriteString("_")
			i = end + 1
			continue
		}

		// Italic: _..._
		if runes[i] == '_' {
			end := -1
			for j := i + 1; j < n; j++ {
				if runes[j] == '_' {
					end = j
					break
				}
			}
			if end == -1 {
				out.WriteString(escapeChar(runes[i]))
				i++
				continue
			}
			inner := markdownToMDV2(string(runes[i+1 : end]))
			out.WriteString("_")
			out.WriteString(inner)
			out.WriteString("_")
			i = end + 1
			continue
		}

		// Strikethrough: ~~...~~
		if i+1 < n && runes[i] == '~' && runes[i+1] == '~' {
			end := -1
			for j := i + 2; j+1 < n; j++ {
				if runes[j] == '~' && runes[j+1] == '~' {
					end = j
					break
				}
			}
			if end == -1 {
				out.WriteString(escapeChar(runes[i]))
				i++
				continue
			}
			inner := markdownToMDV2(string(runes[i+2 : end]))
			out.WriteString("~")
			out.WriteString(inner)
			out.WriteString("~")
			i = end + 2
			continue
		}

		// Link: [text](url)
		if runes[i] == '[' {
			// Find closing ]
			textEnd := -1
			for j := i + 1; j < n; j++ {
				if runes[j] == ']' {
					textEnd = j
					break
				}
			}
			if textEnd != -1 && textEnd+1 < n && runes[textEnd+1] == '(' {
				urlEnd := -1
				for j := textEnd + 2; j < n; j++ {
					if runes[j] == ')' {
						urlEnd = j
						break
					}
				}
				if urlEnd != -1 {
					linkText := markdownToMDV2(string(runes[i+1 : textEnd]))
					url := escapeURL(string(runes[textEnd+2 : urlEnd]))
					out.WriteString("[")
					out.WriteString(linkText)
					out.WriteString("](")
					out.WriteString(url)
					out.WriteString(")")
					i = urlEnd + 1
					continue
				}
			}
			// Not a valid link — escape the bracket
			out.WriteString(escapeChar(runes[i]))
			i++
			continue
		}

		// Plain text character
		out.WriteString(escapeChar(runes[i]))
		i++
	}

	return out.String()
}

// splitChunks splits a MarkdownV2 string into chunks within the 4096 UTF-16 limit.
// Splits prefer newline boundaries to avoid breaking formatting mid-line.
func splitChunks(text string) []MessageChunk {
	if UTF16Len(text) <= maxChunkUTF16 {
		return []MessageChunk{{Text: text}}
	}

	runes := []rune(text)
	// Precompute cumulative UTF-16 lengths.
	utf16At := make([]int, len(runes)+1)
	acc := 0
	for i, r := range runes {
		utf16At[i] = acc
		if r >= 0x10000 {
			acc += 2
		} else {
			acc++
		}
	}
	utf16At[len(runes)] = acc

	var chunks []MessageChunk
	startRune := 0
	for startRune < len(runes) {
		chunkStartUTF16 := utf16At[startRune]
		maxEndUTF16 := chunkStartUTF16 + maxChunkUTF16

		// Find the furthest rune that fits.
		splitAt := len(runes)
		for i := startRune; i < len(runes); i++ {
			if utf16At[i+1] > maxEndUTF16 {
				splitAt = i
				break
			}
		}

		// Prefer splitting at a newline boundary.
		if splitAt < len(runes) {
			bestSplit := splitAt
			for i := startRune; i < splitAt; i++ {
				if runes[i] == '\n' {
					if i+1 < len(runes) && runes[i+1] == '\n' {
						bestSplit = i + 2
					} else {
						bestSplit = i + 1
					}
				}
			}
			splitAt = bestSplit
		}

		chunks = append(chunks, MessageChunk{Text: string(runes[startRune:splitAt])})
		startRune = splitAt
	}
	return chunks
}
