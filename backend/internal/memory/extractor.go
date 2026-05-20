// Package memory — extractor.go: L1 atomic-memory extraction.
//
// The extractor takes a small batch of recent conversation messages and asks
// the LLM to:
//
//  1. Decide ONE scene tag for the whole batch ("AI 和用户在讨论咖啡器材").
//     This is just a hint — the actual L2 scene routing happens in the
//     scene-fitter later. Having it here saves an extra LLM round-trip on
//     the common case where the new atoms all belong together.
//
//  2. Emit zero or more atomic memories, each tagged with a `kind`:
//
//     - persona     stable user attribute       ("用户每天 6:30 起床晨跑")
//     - episodic    one-shot event with date    ("2026-05-19 用户完成项目交付")
//     - instruction long-term rule for the AI    ("以后都叫我猫猫，不要叫全名")
//
//  3. For episodic events, optionally fill activity_start / activity_end in
//     ISO-8601 format. Anything ambiguous is left null.
//
// The prompt is in CHINESE on purpose: the chat history we hand it is
// Chinese, the personas are Chinese, and the down-stream usage is Chinese.
// Mixing English instructions has been shown (empirically and per the
// TencentDB-Agent-Memory ablations) to lower extraction precision in this
// setting.

package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/opentheone/opentheone/backend/internal/llm"
)

// ExtractedAtom is the LLM-emitted candidate before it goes through the
// dedup decision step.
type ExtractedAtom struct {
	Kind          string     `json:"kind"`
	Content       string     `json:"content"`
	Importance    int        `json:"importance"`
	ActivityStart *time.Time `json:"activity_start,omitempty"`
	ActivityEnd   *time.Time `json:"activity_end,omitempty"`
}

// ExtractResult is the full LLM output: scene tag plus N atoms.
type ExtractResult struct {
	SceneName string          `json:"scene_name"`
	Atoms     []ExtractedAtom `json:"atoms"`
}

// ExtractAtoms runs the L1 extractor on a snippet (typically the last 4-8
// turns of a conversation, formatted as "USER: …\nASSISTANT: …" lines).
//
// On a no-content snippet (whitespace-only or LLM returned an empty atom
// list) returns (nil, nil) — caller should treat that as "nothing new to
// remember", not an error.
func (s *Service) ExtractAtoms(ctx context.Context, client *llm.Client, snippet string) (*ExtractResult, error) {
	snippet = strings.TrimSpace(snippet)
	if snippet == "" {
		return nil, nil
	}
	if client == nil {
		// Manual-add path: no LLM available. Caller should use IngestManual
		// directly; we don't fabricate atoms.
		return nil, errors.New("memory.ExtractAtoms: nil llm client")
	}
	msgs := []llm.ChatMessage{
		{Role: "system", Content: extractorSystemPrompt},
		{Role: "user", Content: "请从下面的对话片段中抽取长期记忆。直接输出 JSON，不要写解释，也不要包裹 ```json。\n\n对话片段：\n" + snippet},
	}
	raw, err := client.Chat(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("memory.ExtractAtoms: chat: %w", err)
	}
	raw = stripJSONFence(raw)
	var r ExtractResult
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, fmt.Errorf("memory.ExtractAtoms: parse json: %w (raw=%q)", err, raw)
	}
	// Discard atoms with empty content; clamp importance to [1, 10].
	clean := make([]ExtractedAtom, 0, len(r.Atoms))
	for _, a := range r.Atoms {
		a.Content = strings.TrimSpace(a.Content)
		if a.Content == "" {
			continue
		}
		if a.Importance <= 0 {
			a.Importance = 5
		}
		if a.Importance > 10 {
			a.Importance = 10
		}
		a.Kind = normaliseKind(a.Kind)
		clean = append(clean, a)
	}
	r.Atoms = clean
	r.SceneName = strings.TrimSpace(r.SceneName)
	return &r, nil
}

// stripJSONFence is the standard "the model wrapped its JSON in ```json
// ... ``` even though I asked it not to" recovery.
func stripJSONFence(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		// Drop the first line ("```json" or "```") and the trailing "```".
		if idx := strings.Index(raw, "\n"); idx >= 0 {
			raw = raw[idx+1:]
		}
		raw = strings.TrimSuffix(strings.TrimSpace(raw), "```")
	}
	return strings.TrimSpace(raw)
}

const extractorSystemPrompt = `你是 OpenTheOne 的「长期记忆抽取器」。你的任务是从一段最近的对话片段中，
为这位「用户」抽取值得长期记住的事实，并给所在对话打一个场景标签。

【输出格式】
严格输出一个 JSON 对象，且仅一个，结构如下：
{
  "scene_name": "一句话场景名（≤20字，例：‘AI 在和用户聊咖啡器材’）",
  "atoms": [
    {
      "kind": "persona | episodic | instruction",
      "content": "一条原子级事实（≤80字，一个事实一条，不要堆叠多个事实）",
      "importance": 1-10 的整数,
      "activity_start": "可选，ISO8601 时间，仅 episodic 且能确定时间时填",
      "activity_end": "可选，ISO8601 时间，仅 episodic 且能确定时间时填"
    }
  ]
}

【kind 取值含义】
- persona：用户的稳定属性、长期偏好、身份背景。例：「用户是产品经理」「用户喜欢手冲咖啡」。
- episodic：可以打上时间戳的具体事件。例：「2026-05-19 用户完成了 OTO 项目交付」。
- instruction：用户对 AI 角色的长期指令、口味、规则。例：「以后都叫我猫猫，不要叫全名」。

【抽取规则】
1. 只抽「值得长期记住」的内容，闲聊、寒暄、瞬时情绪一律不抽。
2. 一条 atom 只能含一个事实，宁可拆细，不要合并。
3. content 必须是陈述句，不要包含「用户说」「他告诉我」之类的转述词，直接写事实。
4. importance：1-3 普通偏好/小事；4-6 显著偏好/明确事件；7-9 重要身份信息/强指令；10 留给极个别核心档案。
5. 如果片段中没有任何值得记的内容，返回 {"scene_name": "", "atoms": []}。
6. scene_name 描述「AI 正在和用户做什么」，不要写成「关于咖啡的对话」这类被动表述。
7. 不要凭空补全，不要推测，只写对话中明确出现的事实。

【示例】
对话片段：
USER: 我今天下班去超市了，买了点豆子，明早准备手冲。
ASSISTANT: 哇好棒～你最近都几点起？
USER: 一般 6:30 起来，习惯了。叫我猫猫就行，全名好正式。

输出：
{"scene_name":"AI 和猫猫聊咖啡习惯","atoms":[
  {"kind":"persona","content":"用户喜欢手冲咖啡","importance":6},
  {"kind":"persona","content":"用户习惯每天 6:30 起床","importance":5},
  {"kind":"instruction","content":"称呼用户为「猫猫」，不要使用全名","importance":8}
]}

直接输出 JSON。`
