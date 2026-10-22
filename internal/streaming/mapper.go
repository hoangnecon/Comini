package streaming

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"comini/internal/domain"
	"comini/internal/session"
)

type StreamOptions struct {
	Context    context.Context
	ImageGenFn func(ctx context.Context, args domain.ImageGenArgs) (savedPath string, dataURL string, err error)
	ContinueFn func(ctx context.Context, modelPart domain.GeminiPart, observationText string) (io.ReadCloser, error)
	// CanViewImage reports whether the client declared Codex's native `view_image` tool.
	// When true, a generated image is handed back to Codex as a regular `view_image`
	// function_call (rendered natively by the UI) instead of being looped inside one
	// HTTP response.
	CanViewImage bool
	// KeepaliveInterval overrides the idle interval after which an SSE comment frame is sent.
	// Zero uses defaultKeepaliveInterval.
	KeepaliveInterval time.Duration
}

const defaultKeepaliveInterval = 10 * time.Second

// guardedWriter serialises all writes and flushes to the client so that the keepalive
// goroutine and the mapper never interleave bytes of two SSE frames.
type guardedWriter struct {
	mu      sync.Mutex
	w       io.Writer
	flusher func()
	last    time.Time
}

func (g *guardedWriter) Write(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.last = time.Now()
	return g.w.Write(p)
}

func (g *guardedWriter) flush() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.flusher != nil {
		g.flusher()
	}
}

// keepalive writes an SSE comment frame when the stream has been idle for at least `idle`.
// SSE comments are ignored by the Codex parser but reset its idle timeout and keep
// intermediaries from closing a quiet connection. It returns false if the write failed.
func (g *guardedWriter) keepalive(idle time.Duration) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if time.Since(g.last) < idle {
		return true
	}
	if _, err := io.WriteString(g.w, ": keepalive\n\n"); err != nil {
		return false
	}
	g.last = time.Now()
	if g.flusher != nil {
		g.flusher()
	}
	return true
}

func randomID(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b))
}

func formatSSEWithSeq(eventType string, data map[string]any, seq int) string {
	data["type"] = eventType
	data["sequence_number"] = seq
	b, _ := json.Marshal(data)
	return fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(b))
}

// MapGeminiStreamToResponses parses raw Gemini SSE stream and writes Responses API events to w.
func MapGeminiStreamToResponses(
	r io.Reader,
	w io.Writer,
	flusher func(),
	store *session.SignatureStore,
	responseID string,
	modelName string,
	toolMetaMap map[string]domain.ToolMeta,
	sessionID string,
	opts ...StreamOptions,
) error {
	var streamOpts StreamOptions
	if len(opts) > 0 {
		streamOpts = opts[0]
	}
	if responseID == "" {
		responseID = randomID("resp")
	}
	if modelName == "" {
		modelName = "gemini-3.8-flash"
	}

	createdAt := time.Now().Unix()
	currentReader := r
	defer func() {
		if rc, ok := currentReader.(io.Closer); ok {
			_ = rc.Close()
		}
	}()
	scanner := bufio.NewScanner(currentReader)
	buf := make([]byte, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024)
	seq := 0
	outputIndex := 0

	var currentReasoningID string
	var reasoningIndex int
	var summaryParts []string
	var accumulatedReasoning strings.Builder

	var currentMessageID string
	var messageIndex int
	var accumulatedMessage strings.Builder
	msgFilter := newStreamFilter()

	var pendingThoughtSig string
	var completedOutputItems []map[string]any

	type generatedImageRecord struct {
		Prompt    string
		SavedPath string
	}
	var allGeneratedImages []generatedImageRecord
	var allEmittedText strings.Builder
	hasEmittedMessage := false
	loopCount := 0

	var inputTokens, outputTokens, reasoningTokens, cachedTokens int

	// Upstream timing diagnostics (logged at the end): lets us measure, from comini.log alone,
	// whether a "frozen" UI was upstream silence (e.g. a large tool call or hidden reasoning).
	streamStart := time.Now()
	lastChunkAt := streamStart
	var firstChunkGap, maxChunkGap time.Duration
	chunkCount := 0
	var lastGap time.Duration

	// All client writes go through gw so the keepalive goroutine below can safely
	// emit SSE comment frames while this goroutine is blocked on upstream or image I/O.
	gw := &guardedWriter{w: w, flusher: flusher, last: time.Now()}
	w = gw
	keepaliveEvery := streamOpts.KeepaliveInterval
	if keepaliveEvery <= 0 {
		keepaliveEvery = defaultKeepaliveInterval
	}
	stopKeepalive := make(chan struct{})
	keepaliveDone := make(chan struct{})
	// Stop and join the goroutine before returning so no write can race with the caller
	// reading the destination after this function returns.
	defer func() {
		close(stopKeepalive)
		<-keepaliveDone
	}()
	go func() {
		defer close(keepaliveDone)
		tick := time.NewTicker(keepaliveEvery / 2)
		defer tick.Stop()
		for {
			select {
			case <-stopKeepalive:
				return
			case <-tick.C:
				if !gw.keepalive(keepaliveEvery) {
					return
				}
			}
		}
	}()

	flushSSE := func() {
		gw.flush()
	}

	var writeErr error
	emitSSE := func(eventType string, data map[string]any) {
		if writeErr != nil {
			return
		}
		currentSeq := seq
		seq++
		_, writeErr = io.WriteString(w, formatSSEWithSeq(eventType, data, currentSeq))
	}

	emitCommentaryMessage := func(thoughtText string) {
		thoughtText = strings.TrimSpace(thoughtText)
		if len(thoughtText) < 2 {
			return
		}
		comID := randomID("msg")
		idx := outputIndex
		outputIndex++

		emitSSE("response.output_item.added", map[string]any{
			"output_index": idx,
			"item": map[string]any{
				"id":      comID,
				"type":    "message",
				"role":    "assistant",
				"phase":   "commentary",
				"status":  "in_progress",
				"content": []any{},
			},
		})

		emitSSE("response.content_part.added", map[string]any{
			"output_index":  idx,
			"item_id":       comID,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		})

		emitSSE("response.output_text.delta", map[string]any{
			"output_index":  idx,
			"item_id":       comID,
			"content_index": 0,
			"delta":         thoughtText,
		})

		emitSSE("response.output_text.done", map[string]any{
			"output_index":  idx,
			"item_id":       comID,
			"content_index": 0,
			"text":          thoughtText,
		})

		emitSSE("response.content_part.done", map[string]any{
			"output_index":  idx,
			"item_id":       comID,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        thoughtText,
				"annotations": []any{},
			},
		})

		msgItem := map[string]any{
			"id":     comID,
			"type":   "message",
			"role":   "assistant",
			"phase":  "commentary",
			"status": "completed",
			"content": []any{
				map[string]any{
					"type":        "output_text",
					"text":        thoughtText,
					"annotations": []any{},
				},
			},
		}
		completedOutputItems = append(completedOutputItems, msgItem)
		emitSSE("response.output_item.done", map[string]any{
			"output_index": idx,
			"item":         msgItem,
		})
		allEmittedText.WriteString(thoughtText)
		hasEmittedMessage = true
		flushSSE()
	}

	// closeReasoning completes the open reasoning item. Summary parts carry ONLY concise bold
	// headlines (for the 1-line shimmer title span), while raw content parts carry the full thought.
	// For Codex Desktop, an assistant message with phase: "commentary" is emitted so that the
	// thought block is permanently and sequentially rendered in the conversation timeline.
	closeReasoning := func() {
		if currentReasoningID == "" {
			return
		}
		rawReasoning := strings.TrimSpace(accumulatedReasoning.String())
		summary := make([]any, 0, len(summaryParts))
		for i, partText := range summaryParts {
			partText = strings.TrimSpace(partText)
			if partText == "" {
				continue
			}
			summary = append(summary, map[string]any{"type": "summary_text", "text": partText})
			emitSSE("response.reasoning_summary_text.done", map[string]any{
				"output_index":  reasoningIndex,
				"item_id":       currentReasoningID,
				"summary_index": i,
				"text":          partText,
			})
			emitSSE("response.reasoning_summary_part.done", map[string]any{
				"output_index":  reasoningIndex,
				"item_id":       currentReasoningID,
				"summary_index": i,
				"part":          map[string]any{"type": "summary_text", "text": partText},
			})
		}
		if len(summary) == 0 && rawReasoning != "" {
			headline := extractReasoningHeadline(rawReasoning)
			titlePart := "**" + headline + "**"
			summary = append(summary, map[string]any{"type": "summary_text", "text": titlePart})
			emitSSE("response.reasoning_summary_part.added", map[string]any{
				"output_index":  reasoningIndex,
				"item_id":       currentReasoningID,
				"summary_index": 0,
				"part":          map[string]any{"type": "summary_text", "text": ""},
			})
			emitSSE("response.reasoning_summary_text.delta", map[string]any{
				"output_index":  reasoningIndex,
				"item_id":       currentReasoningID,
				"summary_index": 0,
				"delta":         titlePart,
			})
			emitSSE("response.reasoning_summary_text.done", map[string]any{
				"output_index":  reasoningIndex,
				"item_id":       currentReasoningID,
				"summary_index": 0,
				"text":          titlePart,
			})
			emitSSE("response.reasoning_summary_part.done", map[string]any{
				"output_index":  reasoningIndex,
				"item_id":       currentReasoningID,
				"summary_index": 0,
				"part":          map[string]any{"type": "summary_text", "text": titlePart},
			})
		}

		reasoningContent := make([]any, 0, 1)
		if rawReasoning != "" {
			reasoningContent = append(reasoningContent, map[string]any{
				"type": "reasoning_text",
				"text": rawReasoning,
			})
		}

		reasoningItem := map[string]any{
			"id":      currentReasoningID,
			"type":    "reasoning",
			"status":  "completed",
			"summary": summary,
			"content": reasoningContent,
		}
		completedOutputItems = append(completedOutputItems, reasoningItem)
		emitSSE("response.output_item.done", map[string]any{
			"output_index": reasoningIndex,
			"item":         reasoningItem,
		})

		currentReasoningID = ""
		summaryParts = nil
		accumulatedReasoning.Reset()
		flushSSE()

		// Codex Desktop suppresses 'reasoning' items from the chat timeline (`case 'reasoning': return null;`).
		// Emitting an assistant message with phase: "commentary" renders the thought block permanently
		// and visibly in the chat timeline, immediately before the upcoming tool call or final response.
		if rawReasoning != "" && accumulatedMessage.Len() == 0 {
			emitCommentaryMessage(rawReasoning)
		}
	}

	closeMessage := func(phase string) {
		if phase == "" {
			phase = "final_answer"
		}
		if currentMessageID == "" {
			rem := msgFilter.flush()
			if len(rem) == 0 {
				return
			}
			currentMessageID = randomID("msg")
			messageIndex = outputIndex
			outputIndex++
			accumulatedMessage.Reset()

			emitSSE("response.output_item.added", map[string]any{
				"output_index": messageIndex,
				"item": map[string]any{
					"id":      currentMessageID,
					"type":    "message",
					"role":    "assistant",
					"phase":   phase,
					"status":  "in_progress",
					"content": []any{},
				},
			})

			emitSSE("response.content_part.added", map[string]any{
				"output_index":  messageIndex,
				"item_id":       currentMessageID,
				"content_index": 0,
				"part": map[string]any{
					"type":        "output_text",
					"text":        "",
					"annotations": []any{},
				},
			})

			accumulatedMessage.WriteString(rem)
			emitSSE("response.output_text.delta", map[string]any{
				"output_index":  messageIndex,
				"item_id":       currentMessageID,
				"content_index": 0,
				"delta":         rem,
			})
			flushSSE()
		} else {
			rem := msgFilter.flush()
			if len(rem) > 0 {
				accumulatedMessage.WriteString(rem)
				emitSSE("response.output_text.delta", map[string]any{
					"output_index":  messageIndex,
					"item_id":       currentMessageID,
					"content_index": 0,
					"delta":         rem,
				})
			}
		}
		fullMsg := accumulatedMessage.String()
		allEmittedText.WriteString(fullMsg)

		emitSSE("response.output_text.done", map[string]any{
			"output_index":  messageIndex,
			"item_id":       currentMessageID,
			"content_index": 0,
			"text":          fullMsg,
		})

		emitSSE("response.content_part.done", map[string]any{
			"output_index":  messageIndex,
			"item_id":       currentMessageID,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        fullMsg,
				"annotations": []any{},
			},
		})

		msgItem := map[string]any{
			"id":     currentMessageID,
			"type":   "message",
			"role":   "assistant",
			"phase":  phase,
			"status": "completed",
			"content": []any{
				map[string]any{
					"type":        "output_text",
					"text":        fullMsg,
					"annotations": []any{},
				},
			},
		}
		completedOutputItems = append(completedOutputItems, msgItem)

		emitSSE("response.output_item.done", map[string]any{
			"output_index": messageIndex,
			"item":         msgItem,
		})

		currentMessageID = ""
		accumulatedMessage.Reset()
		msgFilter.reset()
		flushSSE()
	}

	// streamThought streams one chunk of model reasoning. Raw reasoning is streamed directly
	// to response.reasoning_text.delta (populating ThreadItem::Reasoning.content). Summary parts
	// receive ONLY concise bold headlines for Codex Desktop's 1-line shimmer title span,
	// strictly preventing multi-line markdown overflow on the title bar.
	streamThought := func(text string) {
		if currentReasoningID == "" {
			currentReasoningID = randomID("rs")
			reasoningIndex = outputIndex
			outputIndex++
			accumulatedReasoning.Reset()
			summaryParts = nil
			emitSSE("response.output_item.added", map[string]any{
				"output_index": reasoningIndex,
				"item": map[string]any{
					"id":      currentReasoningID,
					"type":    "reasoning",
					"status":  "in_progress",
					"summary": []any{},
					"content": []any{},
				},
			})
		}

		accumulatedReasoning.WriteString(text)

		// 1. Stream raw thought into reasoning_text
		emitSSE("response.reasoning_text.delta", map[string]any{
			"output_index":  reasoningIndex,
			"item_id":       currentReasoningID,
			"content_index": 0,
			"delta":         text,
		})

		// 2. Manage summary headline (for the 1-line shimmer title bar)
		startsBlock := strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "**")
		if len(summaryParts) == 0 {
			acc := accumulatedReasoning.String()
			if len(acc) >= 15 || strings.Contains(acc, "\n") || startsBlock {
				headline := extractReasoningHeadline(acc)
				titlePart := "**" + headline + "**"
				summaryParts = append(summaryParts, titlePart)
				emitSSE("response.reasoning_summary_part.added", map[string]any{
					"output_index":  reasoningIndex,
					"item_id":       currentReasoningID,
					"summary_index": 0,
					"part":          map[string]any{"type": "summary_text", "text": ""},
				})
				emitSSE("response.reasoning_summary_text.delta", map[string]any{
					"output_index":  reasoningIndex,
					"item_id":       currentReasoningID,
					"summary_index": 0,
					"delta":         titlePart,
				})
			}
		} else if startsBlock && strings.TrimSpace(summaryParts[len(summaryParts)-1]) != "" {
			idx := len(summaryParts)
			headline := extractReasoningHeadline(text)
			titlePart := "**" + headline + "**"
			summaryParts = append(summaryParts, titlePart)
			emitSSE("response.reasoning_summary_part.added", map[string]any{
				"output_index":  reasoningIndex,
				"item_id":       currentReasoningID,
				"summary_index": idx,
				"part":          map[string]any{"type": "summary_text", "text": ""},
			})
			emitSSE("response.reasoning_summary_text.delta", map[string]any{
				"output_index":  reasoningIndex,
				"item_id":       currentReasoningID,
				"summary_index": idx,
				"delta":         titlePart,
			})
		}
		flushSSE()
	}

	viewedImages := map[string]bool{}

	// emitFunctionCall emits a complete client-side function_call output item (no Gemini
	// signature: the call was synthesised by the proxy, not produced by the model).
	emitFunctionCall := func(name, argsStr string) {
		callID := randomID("call")
		fcItemID := randomID("fc")
		idx := outputIndex
		outputIndex++
		fcItem := map[string]any{
			"id":        fcItemID,
			"call_id":   callID,
			"type":      "function_call",
			"name":      name,
			"arguments": argsStr,
			"status":    "completed",
		}
		completedOutputItems = append(completedOutputItems, fcItem)
		emitSSE("response.output_item.added", map[string]any{
			"output_index": idx,
			"item": map[string]any{
				"id": fcItemID, "call_id": callID, "type": "function_call",
				"name": name, "arguments": "", "status": "in_progress",
			},
		})
		emitSSE("response.function_call_arguments.delta", map[string]any{
			"output_index": idx, "item_id": fcItemID, "call_id": callID, "delta": argsStr,
		})
		emitSSE("response.function_call_arguments.done", map[string]any{
			"output_index": idx, "item_id": fcItemID, "call_id": callID, "name": name, "arguments": argsStr,
		})
		emitSSE("response.output_item.done", map[string]any{"output_index": idx, "item": fcItem})
	}

	// 1. Emit initial response.created
	emitSSE("response.created", map[string]any{
		"response": map[string]any{
			"id":         responseID,
			"object":     "response",
			"created_at": createdAt,
			"status":     "in_progress",
			"model":      modelName,
			"output":     []any{},
			"usage":      nil,
		},
	})
	flushSSE()

scanLoop:
	for scanner.Scan() {
		if writeErr != nil {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}

		payloadStr := strings.TrimSpace(line[5:])
		if payloadStr == "" || payloadStr == "[DONE]" {
			continue
		}

		var root domain.CCARootChunk
		if err := json.Unmarshal([]byte(payloadStr), &root); err != nil {
			continue
		}

		lastGap = time.Since(lastChunkAt)
		lastChunkAt = time.Now()
		if chunkCount == 0 {
			firstChunkGap = lastGap
			if root.Response != nil && root.Response.ModelVersion != "" {
				log.Printf("[Comini Stream] 🔎 upstream served modelVersion=%s (requested=%s)", root.Response.ModelVersion, modelName)
			}
		}
		if lastGap > maxChunkGap {
			maxChunkGap = lastGap
		}
		chunkCount++

		// Extract candidates and usage
		var candidates []domain.GeminiCandidate
		if root.Response != nil && len(root.Response.Candidates) > 0 {
			candidates = root.Response.Candidates
			if root.Response.UsageMetadata != nil {
				inputTokens = root.Response.UsageMetadata.PromptTokenCount
				outputTokens = root.Response.UsageMetadata.CandidatesTokenCount
				reasoningTokens = root.Response.UsageMetadata.ThoughtsTokenCount
				if root.Response.UsageMetadata.CachedContentTokenCount > 0 {
					cachedTokens = root.Response.UsageMetadata.CachedContentTokenCount
				}
			}
		} else if len(root.Candidates) > 0 {
			candidates = root.Candidates
			if root.UsageMetadata != nil {
				inputTokens = root.UsageMetadata.PromptTokenCount
				outputTokens = root.UsageMetadata.CandidatesTokenCount
				reasoningTokens = root.UsageMetadata.ThoughtsTokenCount
				if root.UsageMetadata.CachedContentTokenCount > 0 {
					cachedTokens = root.UsageMetadata.CachedContentTokenCount
				}
			}
		}

		if len(candidates) == 0 && root.Response != nil && root.Response.UsageMetadata != nil {
			inputTokens = root.Response.UsageMetadata.PromptTokenCount
			outputTokens = root.Response.UsageMetadata.CandidatesTokenCount
			reasoningTokens = root.Response.UsageMetadata.ThoughtsTokenCount
			if root.Response.UsageMetadata.CachedContentTokenCount > 0 {
				cachedTokens = root.Response.UsageMetadata.CachedContentTokenCount
			}
		}

		for _, cand := range candidates {
			for _, part := range cand.Content.Parts {
				// Record thought signature
				if part.ThoughtSignature != "" {
					pendingThoughtSig = part.ThoughtSignature
				}

				// A. Thinking / Reasoning Part
				// A. Thinking / Reasoning Part
				if part.Thought && len(part.Text) > 0 {
					streamThought(part.Text)
					continue
				}

				// Transition out of reasoning
				if currentReasoningID != "" && !part.Thought {
					closeReasoning()
				}

				// B. Standard Text Content Part
				textDelta, actionCall := msgFilter.feed(part.Text)
				if actionCall != nil {
					part.FunctionCall = actionCall
				}
				if part.FunctionCall == nil && len(textDelta) > 0 {
					// Intercept plain text action format and promote to structured FunctionCall
					if fnName, fnArgs, ok := extractActionFromText(textDelta); ok {
						part.FunctionCall = &domain.GeminiFunctionCall{
							Name: fnName,
							Args: fnArgs,
						}
						textDelta = ""
					} else if fnName, fnArgs, ok := extractImageGenFromText(textDelta); ok {
						part.FunctionCall = &domain.GeminiFunctionCall{
							Name: fnName,
							Args: fnArgs,
						}
						textDelta = ""
					}
				}

				if len(textDelta) > 0 {
					hasEmittedMessage = true
					if currentMessageID == "" {
						currentMessageID = randomID("msg")
						messageIndex = outputIndex
						outputIndex++
						accumulatedMessage.Reset()

						emitSSE("response.output_item.added", map[string]any{
							"output_index": messageIndex,
							"item": map[string]any{
								"id":      currentMessageID,
								"type":    "message",
								"role":    "assistant",
								"status":  "in_progress",
								"content": []any{},
							},
						})

						emitSSE("response.content_part.added", map[string]any{
							"output_index":  messageIndex,
							"item_id":       currentMessageID,
							"content_index": 0,
							"part": map[string]any{
								"type":        "output_text",
								"text":        "",
								"annotations": []any{},
							},
						})
					}

					accumulatedMessage.WriteString(textDelta)

					emitSSE("response.output_text.delta", map[string]any{
						"output_index":  messageIndex,
						"item_id":       currentMessageID,
						"content_index": 0,
						"delta":         textDelta,
					})

					flushSSE()
					if part.FunctionCall == nil {
						continue
					}
				}

				// C. Function / Tool Call Part
				if part.FunctionCall != nil {
					if lastGap >= 5*time.Second {
						argBytes := 0
						if b, err := json.Marshal(part.FunctionCall.Args); err == nil {
							argBytes = len(b)
						}
						log.Printf("[Comini Stream] ⏱ functionCall %q arrived after %.1fs of upstream silence (args=%dB)",
							part.FunctionCall.Name, lastGap.Seconds(), argBytes)
					}
					closeReasoning()
					closeMessage("commentary")

					callID := randomID("call")
					fcIndex := outputIndex
					outputIndex++

					geminiName := part.FunctionCall.Name
					meta, hasMeta := toolMetaMap[geminiName]
					if !hasMeta {
						if m, ok := toolMetaMap[strings.ReplaceAll(geminiName, ":", "__")]; ok {
							meta, hasMeta = m, true
						} else if m, ok := toolMetaMap[strings.ReplaceAll(geminiName, "__", ":")]; ok {
							meta, hasMeta = m, true
						} else if strings.HasPrefix(geminiName, "mcp__") {
							if m, ok := toolMetaMap[strings.TrimPrefix(geminiName, "mcp__")]; ok {
								meta, hasMeta = m, true
							}
						} else if m, ok := toolMetaMap["mcp__"+geminiName]; ok {
							meta, hasMeta = m, true
						} else if idx := strings.LastIndex(geminiName, ":"); idx != -1 {
							if m, ok := toolMetaMap[geminiName[idx+1:]]; ok {
								meta, hasMeta = m, true
							}
						} else if idx := strings.LastIndex(geminiName, "__"); idx != -1 {
							if m, ok := toolMetaMap[geminiName[idx+2:]]; ok {
								meta, hasMeta = m, true
							}
						}
					}

					origName := geminiName
					namespace := ""
					if hasMeta {
						if meta.OriginalName != "" {
							origName = meta.OriginalName
						}
						namespace = meta.Namespace
					}

					sigToStore := pendingThoughtSig
					if part.ThoughtSignature != "" {
						sigToStore = part.ThoughtSignature
					}

					if (hasMeta && meta.IsCustom) || geminiName == "apply_patch" || origName == "apply_patch" {
						if origName == "" {
							origName = "apply_patch"
						}
						// Freeform custom tool (apply_patch)
						var inputStr string
						if rawInput, ok := part.FunctionCall.Args["input"].(string); ok {
							inputStr = rawInput
						} else {
							b, _ := json.Marshal(part.FunctionCall.Args)
							inputStr = string(b)
						}

						customItemID := randomID("ctc")
						if store != nil {
							store.Set(callID, sigToStore, geminiName)
							store.Set(customItemID, sigToStore, geminiName)
							if sessionID != "" {
								store.SetSessionCall(sessionID, origName, map[string]any{"input": inputStr}, sigToStore)
								store.SetSessionCall(sessionID, geminiName, map[string]any{"input": inputStr}, sigToStore)
							}
						}

						customItem := map[string]any{
							"id":      customItemID,
							"call_id": callID,
							"type":    "custom_tool_call",
							"name":    origName,
							"input":   inputStr,
							"status":  "completed",
						}
						if namespace != "" {
							customItem["namespace"] = namespace
						}
						completedOutputItems = append(completedOutputItems, customItem)

						inProgCustom := map[string]any{
							"id":      customItemID,
							"call_id": callID,
							"type":    "custom_tool_call",
							"name":    origName,
							"input":   "",
							"status":  "in_progress",
						}
						if namespace != "" {
							inProgCustom["namespace"] = namespace
						}

						emitSSE("response.output_item.added", map[string]any{
							"output_index": fcIndex,
							"item":         inProgCustom,
						})

						emitSSE("response.custom_tool_call_input.delta", map[string]any{
							"output_index": fcIndex,
							"item_id":      customItemID,
							"call_id":      callID,
							"delta":        inputStr,
						})

						doneCustom := map[string]any{
							"output_index": fcIndex,
							"item_id":      customItemID,
							"call_id":      callID,
							"name":         origName,
							"input":        inputStr,
						}
						if namespace != "" {
							doneCustom["namespace"] = namespace
						}
						emitSSE("response.custom_tool_call_input.done", doneCustom)

						emitSSE("response.output_item.done", map[string]any{
							"output_index": fcIndex,
							"item":         customItem,
						})

					} else if (hasMeta && meta.IsToolSearch) || geminiName == "tool_search" || origName == "tool_search" {
						// Dedicated Tool Search Call (client discovery expects arguments to be a JSON Object struct SearchToolCallParams)
						searchParams := make(map[string]any)
						if part.FunctionCall.Args != nil {
							for k, v := range part.FunctionCall.Args {
								searchParams[k] = v
							}
						}
						if _, ok := searchParams["query"]; !ok {
							if q, ok := searchParams["q"].(string); ok {
								searchParams["query"] = q
							}
						}
						if q, ok := searchParams["query"].(string); ok {
							cleanQ := strings.TrimPrefix(q, "mcp__")
							cleanQ = strings.ReplaceAll(cleanQ, "__", " ")
							cleanQ = strings.ReplaceAll(cleanQ, ":", " ")
							cleanQ = strings.TrimSpace(cleanQ)
							if cleanQ != "" {
								searchParams["query"] = cleanQ
							}
						}

						tscItemID := randomID("tsc")
						if store != nil {
							store.Set(callID, sigToStore, geminiName)
							store.Set(tscItemID, sigToStore, geminiName)
							if sessionID != "" {
								store.SetSessionCall(sessionID, origName, searchParams, sigToStore)
								store.SetSessionCall(sessionID, geminiName, searchParams, sigToStore)
							}
						}

						tscItem := map[string]any{
							"id":        tscItemID,
							"call_id":   callID,
							"type":      "tool_search_call",
							"status":    "completed",
							"execution": "client",
							"arguments": searchParams,
						}
						completedOutputItems = append(completedOutputItems, tscItem)

						inProgItem := map[string]any{
							"id":        tscItemID,
							"call_id":   callID,
							"type":      "tool_search_call",
							"status":    "in_progress",
							"execution": "client",
							"arguments": searchParams,
						}

						emitSSE("response.output_item.added", map[string]any{
							"output_index": fcIndex,
							"item":         inProgItem,
						})

						emitSSE("response.output_item.done", map[string]any{
							"output_index": fcIndex,
							"item":         tscItem,
						})

					} else if (hasMeta && meta.IsImageGen) || geminiName == "image_generation" || origName == "image_generation" || origName == "generate_image" || geminiName == "image_gen" || origName == "image_gen" {
						// Dedicated Image Generation Call (executed natively via Gemini Imagen)
						genArgs := domain.ParseImageGenArgs(part.FunctionCall.Args)
						prompt := genArgs.Prompt

						igcItemID := randomID("igc")
						if store != nil {
							// Codex echoes only id/status/revised_prompt/result on later turns; keep the
							// full args (image_paths, image_name, aspect_ratio) so history replay is faithful.
							store.SetImageCall(part.FunctionCall.Args, igcItemID, callID)
							store.Set(callID, sigToStore, geminiName)
							store.Set(igcItemID, sigToStore, geminiName)
							if sessionID != "" {
								store.SetSessionCall(sessionID, "image_generation", part.FunctionCall.Args, sigToStore)
								store.SetSessionCall(sessionID, origName, part.FunctionCall.Args, sigToStore)
								store.SetSessionCall(sessionID, geminiName, part.FunctionCall.Args, sigToStore)
							}
						}

						inProgItem := map[string]any{
							"id":      igcItemID,
							"call_id": callID,
							"type":    "image_generation_call",
							"status":  "in_progress",
						}

						emitSSE("response.output_item.added", map[string]any{
							"output_index": fcIndex,
							"item":         inProgItem,
						})
						flushSSE()

						// Live status so the UI shows progress while the image renders (can take 30-60s).
						streamThought(fmt.Sprintf("**Generating image**\n%s\n", truncateRunes(strings.TrimSpace(prompt), 160)))
						flushSSE()

						var savedPath, dataURL string
						var genErr error
						if streamOpts.ImageGenFn != nil {
							callCtx := streamOpts.Context
							if callCtx == nil {
								callCtx = context.Background()
							}
							savedPath, dataURL, genErr = streamOpts.ImageGenFn(callCtx, genArgs)
						} else {
							genErr = fmt.Errorf("image generation client not configured")
						}

						closeReasoning()
						// Codex 0.163 never renders hosted image_generation_call items (measured), and it
						// re-sends `result` in every later request. Keep the item for history but only
						// carry the base64 payload on the legacy path.
						handedToCodex := streamOpts.CanViewImage && genErr == nil && savedPath != ""
						doneResult := dataURL
						if handedToCodex {
							doneResult = ""
						}
						var doneItem map[string]any
						if genErr != nil {
							log.Printf("[Comini ImageGen] ❌ Error generating image: %v", genErr)
							doneItem = map[string]any{
								"id":      igcItemID,
								"call_id": callID,
								"type":    "image_generation_call",
								"status":  "incomplete",
								"failure": genErr.Error(),
							}
						} else {
							log.Printf("[Comini ImageGen] 🎨 Successfully generated image: saved to %s (dataURL len: %d)", savedPath, len(dataURL))
							if store != nil {
								store.SetImageCall(part.FunctionCall.Args, savedPath)
							}
							doneItem = map[string]any{
								"id":             igcItemID,
								"call_id":        callID,
								"type":           "image_generation_call",
								"status":         "completed",
								"revised_prompt": prompt,
								"result":         doneResult,
								"saved_path":     savedPath,
							}
							allGeneratedImages = append(allGeneratedImages, generatedImageRecord{
								Prompt:    prompt,
								SavedPath: savedPath,
							})
						}
						completedOutputItems = append(completedOutputItems, doneItem)

						emitSSE("response.output_item.done", map[string]any{
							"output_index": fcIndex,
							"item":         doneItem,
						})
						flushSSE()

						if handedToCodex {
							// Codex executes view_image locally (native ImageView item in the UI) and
							// sends the image back to the model on the next request.
							viewedImages[savedPath] = true
							emitFunctionCall("view_image", mustJSON(map[string]any{"path": savedPath}))
							flushSSE()
							continue
						}

						// Server-side Autonomous Loop: Continue with Gemini if ContinueFn is provided (unlimited loops)
						if streamOpts.ContinueFn != nil {
							loopCount++
							obsText := fmt.Sprintf("[Observation: tool image_generation succeeded -> saved to: %s. To display this image to the user, include Markdown embed ![%s](%s) in your message]", savedPath, prompt, savedPath)
							if genErr != nil {
								obsText = fmt.Sprintf("[Observation: tool image_generation failed with error: %v]", genErr)
							}

							callCtx := streamOpts.Context
							if callCtx == nil {
								callCtx = context.Background()
							}
							newReader, cErr := streamOpts.ContinueFn(callCtx, domain.GeminiPart{
								FunctionCall:     part.FunctionCall,
								ThoughtSignature: sigToStore,
							}, obsText)

							if cErr == nil && newReader != nil {
								if curCloser, ok := currentReader.(io.Closer); ok {
									_ = curCloser.Close()
								}
								currentReader = newReader
								scanner = bufio.NewScanner(currentReader)
								scanner.Buffer(buf, 20*1024*1024)
								continue
							}
							log.Printf("[Comini Autonomous Loop] ⚠️ Failed to continue stream (%v), falling back to finishing turn", cErr)
						}

						// Fallback: If ContinueFn is not configured or failed, and no message has been emitted yet
						if !hasEmittedMessage {
							msgIndex := outputIndex
							outputIndex++
							msgID := randomID("msg")

							var msgText string
							if genErr != nil {
								msgText = fmt.Sprintf("❌ Không thể tạo ảnh: %v", genErr)
							} else {
								fileName := filepath.Base(savedPath)
								msgText = fmt.Sprintf("Tôi đã tạo ảnh thành công theo yêu cầu của bạn:\n\n![%s](%s)\n\n- **Prompt:** %s\n- **File đã lưu:** [%s](%s)",
									prompt, savedPath, prompt, fileName, savedPath)
							}

							emitSSE("response.output_item.added", map[string]any{
								"output_index": msgIndex,
								"item": map[string]any{
									"id":      msgID,
									"type":    "message",
									"role":    "assistant",
									"phase":   "final_answer",
									"status":  "in_progress",
									"content": []any{},
								},
							})

							emitSSE("response.content_part.added", map[string]any{
								"output_index":  msgIndex,
								"item_id":       msgID,
								"content_index": 0,
								"part": map[string]any{
									"type":        "output_text",
									"text":        "",
									"annotations": []any{},
								},
							})

							emitSSE("response.output_text.delta", map[string]any{
								"output_index":  msgIndex,
								"item_id":       msgID,
								"content_index": 0,
								"delta":         msgText,
							})

							emitSSE("response.output_text.done", map[string]any{
								"output_index":  msgIndex,
								"item_id":       msgID,
								"content_index": 0,
								"text":          msgText,
							})

							emitSSE("response.content_part.done", map[string]any{
								"output_index":  msgIndex,
								"item_id":       msgID,
								"content_index": 0,
								"part": map[string]any{
									"type":        "output_text",
									"text":        msgText,
									"annotations": []any{},
								},
							})

							msgDoneItem := map[string]any{
								"id":     msgID,
								"type":   "message",
								"role":   "assistant",
								"phase":  "final_answer",
								"status": "completed",
								"content": []any{
									map[string]any{
										"type":        "output_text",
										"text":        msgText,
										"annotations": []any{},
									},
								},
							}
							completedOutputItems = append(completedOutputItems, msgDoneItem)
							hasEmittedMessage = true

							emitSSE("response.output_item.done", map[string]any{
								"output_index": msgIndex,
								"item":         msgDoneItem,
							})
							flushSSE()
						}

					} else {
						// Standard function call
						argsJSON, _ := json.Marshal(part.FunctionCall.Args)
						argsStr := string(argsJSON)
						if argsStr == "null" || argsStr == "" {
							argsStr = "{}"
						}

						targetName := origName
						targetNamespace := namespace
						if hasMeta {
							if meta.Namespace != "" {
								targetNamespace = meta.Namespace
								targetName = meta.OriginalName
							} else {
								targetName = meta.OriginalName
							}
						} else {
							if strings.HasPrefix(origName, "mcp__") {
								trimmed := strings.TrimPrefix(origName, "mcp__")
								parts := strings.SplitN(trimmed, "__", 2)
								if len(parts) == 2 {
									targetNamespace = "mcp__" + parts[0]
									targetName = parts[1]
								}
							} else if idx := strings.Index(origName, ":"); idx != -1 {
								targetNamespace = origName[:idx]
								targetName = origName[idx+1:]
							}
						}

						fcItemID := randomID("fc")
						if store != nil {
							store.Set(callID, sigToStore, geminiName)
							store.Set(fcItemID, sigToStore, geminiName)
							if sessionID != "" {
								store.SetSessionCall(sessionID, targetName, part.FunctionCall.Args, sigToStore)
								store.SetSessionCall(sessionID, origName, part.FunctionCall.Args, sigToStore)
								store.SetSessionCall(sessionID, geminiName, part.FunctionCall.Args, sigToStore)
							}
						}

						fcItem := map[string]any{
							"id":        fcItemID,
							"call_id":   callID,
							"type":      "function_call",
							"name":      targetName,
							"arguments": argsStr,
							"status":    "completed",
						}
						if targetNamespace != "" {
							fcItem["namespace"] = targetNamespace
						}
						completedOutputItems = append(completedOutputItems, fcItem)

						inProgItem := map[string]any{
							"id":        fcItemID,
							"call_id":   callID,
							"type":      "function_call",
							"name":      targetName,
							"arguments": "",
							"status":    "in_progress",
						}
						if targetNamespace != "" {
							inProgItem["namespace"] = targetNamespace
						}

						emitSSE("response.output_item.added", map[string]any{
							"output_index": fcIndex,
							"item":         inProgItem,
						})

						emitSSE("response.function_call_arguments.delta", map[string]any{
							"output_index": fcIndex,
							"item_id":      fcItemID,
							"call_id":      callID,
							"delta":        argsStr,
						})

						doneFunc := map[string]any{
							"output_index": fcIndex,
							"item_id":      fcItemID,
							"call_id":      callID,
							"name":         targetName,
							"arguments":    argsStr,
						}
						if targetNamespace != "" {
							doneFunc["namespace"] = targetNamespace
						}
						emitSSE("response.function_call_arguments.done", doneFunc)

						emitSSE("response.output_item.done", map[string]any{
							"output_index": fcIndex,
							"item":         fcItem,
						})
					}

					flushSSE()
				}
			}
		}
	}

	// Close open items
	closeReasoning()
	closeMessage("final_answer")

	// Emit any generated images that were not explicitly embedded by the model in its message text.
	// This guarantees generated images are never swallowed even when the model emits text or tool calls alongside image_generation.
	var unrenderedImages []generatedImageRecord
	emittedSoFar := allEmittedText.String()
	for _, img := range allGeneratedImages {
		if img.SavedPath != "" && !viewedImages[img.SavedPath] && !strings.Contains(emittedSoFar, img.SavedPath) {
			unrenderedImages = append(unrenderedImages, img)
		}
	}
	if len(unrenderedImages) > 0 {
		msgIndex := outputIndex
		outputIndex++
		msgID := randomID("msg")

		var b strings.Builder
		if len(unrenderedImages) == 1 {
			img := unrenderedImages[0]
			fileName := filepath.Base(img.SavedPath)
			b.WriteString(fmt.Sprintf("Tôi đã tạo ảnh thành công theo yêu cầu của bạn:\n\n![%s](%s)\n\n- **Prompt:** %s\n- **File đã lưu:** [%s](%s)",
				img.Prompt, img.SavedPath, img.Prompt, fileName, img.SavedPath))
		} else {
			b.WriteString(fmt.Sprintf("Tôi đã tạo thành công %d ảnh theo yêu cầu:\n\n", len(unrenderedImages)))
			for idx, img := range unrenderedImages {
				fileName := filepath.Base(img.SavedPath)
				b.WriteString(fmt.Sprintf("### Ảnh %d\n![%s](%s)\n- **Prompt:** %s\n- **File:** [%s](%s)\n\n",
					idx+1, img.Prompt, img.SavedPath, img.Prompt, fileName, img.SavedPath))
			}
		}
		msgText := b.String()
		allEmittedText.WriteString(msgText)

		emitSSE("response.output_item.added", map[string]any{
			"output_index": msgIndex,
			"item": map[string]any{
				"id":      msgID,
				"type":    "message",
				"role":    "assistant",
				"phase":   "final_answer",
				"status":  "in_progress",
				"content": []any{},
			},
		})

		emitSSE("response.content_part.added", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		})

		emitSSE("response.output_text.delta", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"delta":         msgText,
		})

		emitSSE("response.output_text.done", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"text":          msgText,
		})

		emitSSE("response.content_part.done", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        msgText,
				"annotations": []any{},
			},
		})

		msgDoneItem := map[string]any{
			"id":     msgID,
			"type":   "message",
			"role":   "assistant",
			"phase":  "final_answer",
			"status": "completed",
			"content": []any{
				map[string]any{
					"type":        "output_text",
					"text":        msgText,
					"annotations": []any{},
				},
			},
		}
		completedOutputItems = append(completedOutputItems, msgDoneItem)
		hasEmittedMessage = true

		emitSSE("response.output_item.done", map[string]any{
			"output_index": msgIndex,
			"item":         msgDoneItem,
		})
		flushSSE()
	}

	if inputTokens == 0 {
		inputTokens = 1
	}
	if outputTokens == 0 {
		outputTokens = 1
	}

	// Safety net: if no actionable items (no final_answer message, no tool call) were emitted,
	// first try continuing via ContinueFn so Gemini emits its next tool call or final response.
	hasActionableItem := false
	for _, it := range completedOutputItems {
		t, _ := it["type"].(string)
		ph, _ := it["phase"].(string)
		if (t == "message" && ph != "commentary") || t == "function_call" || t == "custom_tool_call" || t == "image_generation_call" || t == "tool_search_call" {
			hasActionableItem = true
			break
		}
	}

	if !hasActionableItem && streamOpts.ContinueFn != nil && loopCount < 2 && writeErr == nil {
		loopCount++
		callCtx := streamOpts.Context
		if callCtx == nil {
			callCtx = context.Background()
		}
		newReader, cErr := streamOpts.ContinueFn(callCtx, domain.GeminiPart{
			Text: "...",
		}, "[Observation: Your previous turn produced no tool call or final output. Please immediately invoke the next tool or output your response to complete the user's request.]")
		if cErr == nil && newReader != nil {
			if curCloser, ok := currentReader.(io.Closer); ok {
				_ = curCloser.Close()
			}
			currentReader = newReader
			scanner = bufio.NewScanner(currentReader)
			scanner.Buffer(buf, 20*1024*1024)
			goto scanLoop
		}
	}

	if !hasActionableItem {
		msgIndex := outputIndex
		outputIndex++
		msgID := randomID("msg")

		fallbackText := "Đang tiếp tục xử lý..."

		emitSSE("response.output_item.added", map[string]any{
			"output_index": msgIndex,
			"item": map[string]any{
				"id":      msgID,
				"type":    "message",
				"role":    "assistant",
				"phase":   "final_answer",
				"status":  "in_progress",
				"content": []any{},
			},
		})
		emitSSE("response.content_part.added", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        "",
				"annotations": []any{},
			},
		})
		emitSSE("response.output_text.delta", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"delta":         fallbackText,
		})
		emitSSE("response.output_text.done", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"text":          fallbackText,
		})
		emitSSE("response.content_part.done", map[string]any{
			"output_index":  msgIndex,
			"item_id":       msgID,
			"content_index": 0,
			"part": map[string]any{
				"type":        "output_text",
				"text":        fallbackText,
				"annotations": []any{},
			},
		})
		msgDoneItem := map[string]any{
			"id":     msgID,
			"type":   "message",
			"role":   "assistant",
			"phase":  "final_answer",
			"status": "completed",
			"content": []any{
				map[string]any{
					"type":        "output_text",
					"text":        fallbackText,
					"annotations": []any{},
				},
			},
		}
		completedOutputItems = append(completedOutputItems, msgDoneItem)
		emitSSE("response.output_item.done", map[string]any{
			"output_index": msgIndex,
			"item":         msgDoneItem,
		})
		flushSSE()
	}

	// Terminal event: response.completed with strict Async-OpenAI / Codex deserialization support
	emitSSE("response.completed", map[string]any{
		"response": map[string]any{
			"id":         responseID,
			"object":     "response",
			"created_at": createdAt,
			"status":     "completed",
			"model":      modelName,
			"output":     completedOutputItems,
			"usage": map[string]any{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens + reasoningTokens,
				"total_tokens":  inputTokens + outputTokens + reasoningTokens,
				"input_tokens_details": map[string]any{
					"cached_tokens": cachedTokens,
				},
				"output_tokens_details": map[string]any{
					"reasoning_tokens": reasoningTokens,
				},
			},
		},
	})

	var hitRate float64
	if inputTokens > 0 {
		hitRate = float64(cachedTokens) / float64(inputTokens) * 100
	}
	log.Printf("[Comini] 📊 Tokens: input=%d (cached=%d, hit=%.1f%%) output=%d reasoning=%d",
		inputTokens, cachedTokens, hitRate, outputTokens, reasoningTokens)
	log.Printf("[Comini Stream] ⏱ upstream chunks=%d firstChunk=%.1fs maxGap=%.1fs total=%.1fs",
		chunkCount, firstChunkGap.Seconds(), maxChunkGap.Seconds(), time.Since(streamStart).Seconds())

	// Finalize SSE stream
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	flushSSE()

	return scanner.Err()
}

func extractActionFromText(text string) (string, map[string]any, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "[Action: called ") && !strings.HasPrefix(trimmed, "[Action: ") {
		return "", nil, false
	}
	rest := trimmed
	if strings.HasPrefix(rest, "[Action: called ") {
		rest = strings.TrimPrefix(rest, "[Action: called ")
	} else {
		rest = strings.TrimPrefix(rest, "[Action: ")
	}
	rest = strings.TrimSuffix(rest, "]")

	// Format: "<name> with <args>" or "<name> with args: <args>"
	withIdx := strings.Index(rest, " with ")
	if withIdx == -1 {
		return "", nil, false
	}
	funcName := strings.TrimSpace(rest[:withIdx])
	argsPart := strings.TrimSpace(rest[withIdx+len(" with "):])
	argsPart = strings.TrimPrefix(argsPart, "args:")
	argsPart = strings.TrimSpace(argsPart)

	var argsMap map[string]any
	if err := json.Unmarshal([]byte(argsPart), &argsMap); err == nil && argsMap != nil {
		return funcName, argsMap, true
	}

	// Native support for raw custom tool input (e.g. apply_patch raw diff format)
	if (funcName == "apply_patch" || strings.HasSuffix(funcName, "apply_patch")) && strings.Contains(argsPart, "*** Begin Patch") {
		return funcName, map[string]any{"input": argsPart}, true
	}

	return "", nil, false
}

func extractImageGenFromText(text string) (string, map[string]any, bool) {
	trimmed := strings.TrimSpace(text)
	const prefix = "[Generated Image based on prompt:"
	if !strings.HasPrefix(trimmed, prefix) {
		return "", nil, false
	}
	rest := strings.TrimPrefix(trimmed, prefix)
	rest = strings.TrimSpace(rest)
	rest = strings.TrimSuffix(rest, "]")
	rest = strings.TrimSpace(rest)

	// Check if there is ". Saved to:" suffix
	if idx := strings.Index(rest, ". Saved to:"); idx != -1 {
		rest = strings.TrimSpace(rest[:idx])
	}

	prompt := rest
	if strings.HasPrefix(prompt, "\"") {
		if unquoted, err := strconv.Unquote(prompt); err == nil {
			prompt = unquoted
		} else {
			prompt = strings.Trim(prompt, "\"")
		}
	}

	if prompt == "" {
		return "", nil, false
	}

	aspectRatio := "1:1"
	lowerPrompt := strings.ToLower(prompt)
	if strings.Contains(lowerPrompt, "9:16") {
		aspectRatio = "9:16"
	} else if strings.Contains(lowerPrompt, "16:9") {
		aspectRatio = "16:9"
	} else if strings.Contains(lowerPrompt, "4:3") {
		aspectRatio = "4:3"
	} else if strings.Contains(lowerPrompt, "3:4") {
		aspectRatio = "3:4"
	}

	return "image_generation", map[string]any{
		"prompt":       prompt,
		"aspect_ratio": aspectRatio,
	}, true
}

func hasActionPrefix(s string) bool {
	return strings.HasPrefix(s, "[Action: called ") ||
		strings.HasPrefix(s, "[Action: ") ||
		strings.HasPrefix(s, "[Generated Image based on prompt:")
}

func mayBePartialActionPrefix(s string) bool {
	if len(s) == 0 {
		return false
	}
	const target = "[Action: called "
	if len(s) < len(target) && strings.HasPrefix(target, s) {
		return true
	}
	const alt = "[Action: "
	if len(s) < len(alt) && strings.HasPrefix(alt, s) {
		return true
	}
	const img = "[Generated Image based on prompt:"
	if len(s) < len(img) && strings.HasPrefix(img, s) {
		return true
	}
	return false
}

var stripProtocolPrefixes = []string{
	"[Observation:",
	"[Image omitted",
	"[Active Collaboration",
	"[Active Technical Plan",
	"[User Confirmed",
	"[Previous Conversation",
	"[Generated Image based on prompt:",
}

// findMatchingClosingBracket finds the index of the matching ']' for the '[' at startIndex.
// It properly tracks string literals, escaped quotes, and nested brackets.
// Returns -1 if no matching closing bracket is found in s.
func findMatchingClosingBracket(s string, startIndex int) int {
	if startIndex < 0 || startIndex >= len(s) || s[startIndex] != '[' {
		return -1
	}
	depth := 0
	inString := false
	escaped := false

	for i := startIndex; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if !inString {
			if ch == '[' {
				depth++
			} else if ch == ']' {
				depth--
				if depth == 0 {
					return i
				}
			}
		}
	}
	return -1
}

func hasProtocolPrefix(s string) (string, bool) {
	for _, p := range stripProtocolPrefixes {
		if strings.HasPrefix(s, p) {
			return p, true
		}
	}
	return "", false
}

func mayBePartialProtocolPrefix(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, p := range stripProtocolPrefixes {
		if len(s) < len(p) && strings.HasPrefix(p, s) {
			return true
		}
	}
	return false
}

type streamFilter struct {
	buf     string
	emitted int
}

func newStreamFilter() *streamFilter {
	return &streamFilter{}
}

func (f *streamFilter) feed(chunk string) (string, *domain.GeminiFunctionCall) {
	f.buf += chunk
	var ready strings.Builder
	var actionCall *domain.GeminiFunctionCall

	for len(f.buf) > 0 {
		openIdx := strings.IndexByte(f.buf, '[')
		if openIdx == -1 {
			ready.WriteString(f.buf)
			f.emitted += len(f.buf)
			f.buf = ""
			break
		}

		if openIdx > 0 {
			prefix := f.buf[:openIdx]
			if f.emitted == 0 && strings.TrimSpace(prefix) == "" {
				// Drop leading whitespace preceding a leading protocol or action block
				f.buf = f.buf[openIdx:]
			} else {
				ready.WriteString(prefix)
				f.emitted += len(prefix)
				f.buf = f.buf[openIdx:]
			}
		}

		// Now f.buf starts with '['
		// 1. Check for Action Call format: [Action: called <name> with <args>]
		if hasActionPrefix(f.buf) {
			closeIdx := findMatchingClosingBracket(f.buf, 0)
			if closeIdx == -1 {
				// Action block is still streaming across chunks; wait for closing ']'
				break
			}
			actionBlock := f.buf[:closeIdx+1]
			f.buf = f.buf[closeIdx+1:]
			if fnName, fnArgs, ok := extractActionFromText(actionBlock); ok {
				actionCall = &domain.GeminiFunctionCall{
					Name: fnName,
					Args: fnArgs,
				}
			} else if fnName, fnArgs, ok := extractImageGenFromText(actionBlock); ok {
				actionCall = &domain.GeminiFunctionCall{
					Name: fnName,
					Args: fnArgs,
				}
			}
			if f.emitted == 0 && ready.Len() == 0 {
				f.buf = strings.TrimLeft(f.buf, "\r\n")
			}
			if actionCall != nil {
				// Full action call extracted; return immediately so tool call is dispatched
				break
			}
			// Fallback: action block could not be extracted as a structured tool call.
			// NEVER DROP TEXT: deliver actionBlock as output text so model never goes silent!
			ready.WriteString(actionBlock)
			f.emitted += len(actionBlock)
			continue
		}

		if mayBePartialActionPrefix(f.buf) {
			// Incomplete prefix that could turn into an action call; buffer it
			break
		}

		// 2. Check for Leaked Protocol Observations to strip: [Observation: ...]
		if _, hasPrefix := hasProtocolPrefix(f.buf); hasPrefix {
			closeIdx := findMatchingClosingBracket(f.buf, 0)
			if closeIdx == -1 {
				// Protocol block is incomplete; wait for more stream chunks
				break
			}
			// Complete protocol block found -> drop it!
			f.buf = f.buf[closeIdx+1:]
			if f.emitted == 0 && ready.Len() == 0 {
				f.buf = strings.TrimLeft(f.buf, "\r\n")
			}
			continue
		}

		if mayBePartialProtocolPrefix(f.buf) {
			// Incomplete prefix that could turn into a protocol tag, buffer it
			break
		}

		// Starts with '[' but is NEITHER an Action NOR a Protocol tag (e.g. "[link](...", "[1]", "[x]")
		ready.WriteByte(f.buf[0])
		f.emitted++
		f.buf = f.buf[1:]
	}

	return ready.String(), actionCall
}

func (f *streamFilter) flush() string {
	if len(f.buf) == 0 {
		return ""
	}
	// Try parsing an action at EOF if missing trailing bracket:
	if hasActionPrefix(f.buf) {
		trimmed := strings.TrimSpace(f.buf)
		if !strings.HasSuffix(trimmed, "]") {
			trimmed += "]"
		}
		if _, _, ok := extractActionFromText(trimmed); ok {
			f.buf = ""
			return ""
		}
	}
	if _, hasPrefix := hasProtocolPrefix(f.buf); hasPrefix {
		// Pure protocol leak to strip (e.g. [Observation: ...])
		f.buf = ""
		return ""
	}
	rem := f.buf
	f.buf = ""
	f.emitted += len(rem)
	return rem
}

func (f *streamFilter) reset() {
	f.buf = ""
	f.emitted = 0
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// truncateRunes shortens s to at most n runes, appending an ellipsis when cut.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// normalizeThoughtDelta ensures thought headings ("**Title**\n") have a blank line after them
// so markdown renders them as distinct paragraphs instead of run-on lines.
func normalizeThoughtDelta(text string, atPartStart bool) string {
	if strings.Contains(text, "**\n") && !strings.Contains(text, "**\n\n") {
		text = strings.ReplaceAll(text, "**\n", "**\n\n")
	}
	return text
}

// extractReasoningHeadline extracts a concise bold heading (max ~6 words, up to 45 chars)
// from the opening of a reasoning block, suitable for Codex Desktop's title header bar.
func extractReasoningHeadline(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "Thinking"
	}
	// 1. If text already starts with markdown bold "**Headline**", extract it
	if strings.HasPrefix(text, "**") {
		rest := text[2:]
		if idx := strings.Index(rest, "**"); idx > 0 {
			headline := strings.TrimSpace(rest[:idx])
			if headline != "" {
				return headline
			}
		}
	}

	// 2. Extract from first sentence/clause
	firstSeg := text
	for _, delim := range []string{"\n", ".", "!", "?", ":", ";"} {
		if idx := strings.Index(firstSeg, delim); idx != -1 {
			firstSeg = firstSeg[:idx]
		}
	}
	// Strip LaTeX math delimiters
	firstSeg = strings.ReplaceAll(firstSeg, "$", "")
	words := strings.Fields(firstSeg)
	if len(words) == 0 {
		return "Thinking"
	}
	if len(words) > 6 {
		words = words[:6]
	}

	// Trim trailing English stop words so headline does not end on "regarding the", etc.
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "of": true, "to": true, "in": true,
		"for": true, "on": true, "with": true, "at": true, "by": true, "from": true,
		"up": true, "about": true, "into": true, "over": true, "after": true,
		"regarding": true, "concerning": true, "and": true, "or": true, "but": true,
		"is": true, "are": true, "was": true, "were": true,
	}
	for len(words) > 2 && stopWords[strings.ToLower(words[len(words)-1])] {
		words = words[:len(words)-1]
	}

	headline := strings.Join(words, " ")
	headline = strings.TrimRight(headline, ",;:\"'`.")
	if len(headline) > 45 {
		headline = strings.TrimSpace(headline[:42]) + "..."
	}
	if len(headline) < 3 {
		return "Thinking"
	}
	runes := []rune(headline)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
