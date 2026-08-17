package codex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/chiga0/marshal-harness/internal/port"
)

type protocolPhase uint8

const (
	phaseBeforeThread protocolPhase = iota
	phaseAwaitingTurn
	phaseInTurn
	phaseTerminal
)

// captureResult 聚合一次有界 JSONL 捕获的结构化结果。
type captureResult struct {
	raw            []byte
	threadID       string
	eventCount     int
	turnCount      int
	itemCount      int
	inputTokens    int
	outputTokens   int
	sawTerminal    bool
	phase          protocolPhase
	itemActive     bool
	activeItemType string
	limitExceeded  bool
	err            error
}

// codexFailure 把所有可持久化的 Adapter 失败绑定到 port.AdapterFailure，
// 同时保留旧 sentinel 供 errors.Is 使用。detail 只能来自本包固定词汇，
// 绝不接受 provider 字段、stderr、路径、prompt 或其他自由文本。
type codexFailure struct {
	failure  port.AdapterFailure
	sentinel error
	detail   string
}

func newCodexFailure(kind port.FailureKind, sentinel error, detail string, now time.Time) *codexFailure {
	disposition, _ := port.DispositionFor(kind)
	failure, _ := port.NewAdapterFailure(port.AdapterIDCodex, kind, disposition, nil, nil, now)
	return &codexFailure{failure: failure, sentinel: sentinel, detail: detail}
}

func (e *codexFailure) Error() string {
	if e.detail == "" {
		return e.failure.Error()
	}
	return e.failure.Error() + ": " + e.detail
}

func (e *codexFailure) Unwrap() []error {
	if e.sentinel == nil {
		return []error{e.failure}
	}
	return []error{e.failure, e.sentinel}
}

func codexProtocolFailure(detail string, now time.Time) error {
	return newCodexFailure(port.FailureKindProtocolInvalid, ErrProtocol, detail, now)
}

func codexProviderFailure(now time.Time) error {
	return newCodexFailure(port.FailureKindProviderTerminal, ErrProviderFailed, "provider reported a terminal failure", now)
}

func codexResultFailure(detail string, now time.Time) error {
	return newCodexFailure(port.FailureKindResultMissing, nil, detail, now)
}

// codexEvent 只覆盖 0.145.0 `codex exec --json` 契约中 Marshal 需要校验
// 的已知字段；其余字段被有意忽略，provider 自由文本字段（例如 turn.failed
// 的错误信息）从不被解码，因此不可能进入授权判断、预算或返回的错误。
// ThreadID 使用指针以区分缺省与空串：缺省事件不携带身份，空串身份一律
// fail closed。
type codexEvent struct {
	Type     string  `json:"type"`
	ThreadID *string `json:"thread_id"`
	Item     struct {
		Type string `json:"type"`
	} `json:"item"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// eventThreadID 校验事件携带的 thread 身份：缺省返回空串（不改变绑定），
// 空串或与已绑定 thread 不一致均 fail closed。
func eventThreadID(event *codexEvent, bound string) (string, bool, error) {
	if event.ThreadID == nil {
		return "", false, nil
	}
	if *event.ThreadID == "" {
		return "", false, fmt.Errorf("%w: event carries an empty thread_id", ErrProtocol)
	}
	if bound != "" && *event.ThreadID != bound {
		return "", false, fmt.Errorf("%w: thread identity changed", ErrProtocol)
	}
	return *event.ThreadID, true, nil
}

// captureJSONL 边读边计数地捕获 Codex `exec --json` transcript，并强制执行
// 按 0.145.0 真实输出冻结的协议契约：
//   - 首个事件必须是 thread.started 且携带非空 thread_id；
//   - 只允许单一 attempt/thread：重复 thread.started、后续事件携带不同
//     thread_id 均 fail closed；
//   - 允许真实成功序列 thread.started → turn.started → item.* → turn.completed；
//   - 必须存在明确的成功终态 turn.completed；其后的任何非空事件 fail closed；
//   - provider turn failure（turn.failed）、malformed JSONL、缺首事件、
//     缺终态、未知事件类型均 fail closed；
//   - session/usage/计数只从已知事件字段提取；
//   - 输出有总字节上限，超限（包括无换行长记录）立即经 onLimit 终止整个
//     进程组，并保持 raw 不超过上限字节。
func captureJSONL(reader io.Reader, limit int64, onLimit func()) captureResult {
	capacity := 64 << 10
	if limit < int64(capacity) {
		capacity = int(limit)
	}
	if capacity < 0 {
		capacity = 0
	}
	result := captureResult{raw: make([]byte, 0, capacity)}
	buffered := bufio.NewReaderSize(reader, 64<<10)
	terminated := false
	terminate := func() {
		if !terminated {
			terminated = true
			onLimit()
		}
	}
	fail := func(err error) {
		if result.err == nil {
			result.err = err
		}
		terminate()
	}
	var line []byte
	for {
		fragment, err := buffered.ReadSlice('\n')
		if len(fragment) > 0 {
			remaining := limit - int64(len(result.raw))
			if remaining > 0 {
				take := int64(len(fragment))
				if take > remaining {
					take = remaining
				}
				result.raw = append(result.raw, fragment[:take]...)
			}
			if int64(len(fragment)) > remaining {
				if !result.limitExceeded {
					result.limitExceeded = true
					terminate()
				}
				line = nil
			} else if !result.limitExceeded {
				line = append(line, fragment...)
			}
			complete := !errors.Is(err, bufio.ErrBufferFull)
			if complete && len(line) > 0 && !result.limitExceeded {
				trimmed := bytes.TrimSpace(line)
				line = nil
				if len(trimmed) == 0 {
					continue
				}
				if decodeErr := result.decodeEventLine(trimmed); decodeErr != nil {
					fail(decodeErr)
				}
			}
		}
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			// 进程组被 Marshal 终止后读端会被关闭：该停止是监督动作的
			// 结果而非协议错误，分类交给 Run 的 context/limit/协议优先级。
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) && result.err == nil {
				fail(err)
			}
			if result.err == nil && !result.limitExceeded {
				if result.eventCount == 0 {
					fail(fmt.Errorf("%w: stream ended before the first event", ErrProtocol))
				} else if !result.sawTerminal {
					fail(fmt.Errorf("%w: stream ended without a terminal turn", ErrProtocol))
				}
			}
			return result
		}
	}
}

// decodeEventLine 将一行 JSONL 事件折叠进捕获聚合并执行冻结协议检查；
// 返回的错误只包含稳定分类与协议字段，不回显 provider 自由文本。
func (result *captureResult) decodeEventLine(line []byte) error {
	var event codexEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return fmt.Errorf("%w: malformed JSONL", ErrProtocol)
	}
	result.eventCount++
	if result.eventCount == 1 {
		if event.Type != "thread.started" {
			return fmt.Errorf("%w: first event must be thread.started", ErrProtocol)
		}
		if event.ThreadID == nil || *event.ThreadID == "" {
			return fmt.Errorf("%w: first event is missing the thread identity", ErrProtocol)
		}
		result.threadID = *event.ThreadID
		result.phase = phaseAwaitingTurn
		return nil
	}
	if result.sawTerminal {
		return fmt.Errorf("%w: trailing event after the terminal turn", ErrProtocol)
	}
	identity, present, err := eventThreadID(&event, result.threadID)
	if err != nil {
		return err
	}
	if present {
		result.threadID = identity
	}
	switch event.Type {
	case "thread.started":
		return fmt.Errorf("%w: duplicate thread.started; only one attempt/thread is allowed", ErrProtocol)
	case "turn.started":
		// Codex 0.145.0 的真实 exec --json 事件不保证携带 turn_id；turn
		// 身份不属于 Marshal 需要绑定的会话权威，顺序才是门禁。
		if result.phase != phaseAwaitingTurn {
			return fmt.Errorf("%w: turn.started is out of order", ErrProtocol)
		}
		result.phase = phaseInTurn
		result.turnCount++
	// item 事件收紧为 0.145.0 已证实的精确闭集：item.started/item.updated/
	// item.completed；其他任何 item.* 与未知类型一律 fail closed。
	case "item.started":
		if result.phase != phaseInTurn || result.itemActive {
			return fmt.Errorf("%w: item.started is out of order", ErrProtocol)
		}
		if !supportedItemType(event.Item.Type) {
			return fmt.Errorf("%w: item.started carries an unknown item type", ErrProtocol)
		}
		result.itemActive = true
		result.activeItemType = event.Item.Type
	case "item.updated":
		if result.phase != phaseInTurn || !result.itemActive {
			return fmt.Errorf("%w: item.updated is out of order", ErrProtocol)
		}
		if event.Item.Type != result.activeItemType {
			return fmt.Errorf("%w: item.updated changed the active item type", ErrProtocol)
		}
	case "item.completed":
		// 0.145.0 对 agent_message 等终态型 item 可直接发 completed，
		// 不先发 started；若此前存在 started，则 completed 同时关闭它。
		if result.phase != phaseInTurn {
			return fmt.Errorf("%w: item.completed is out of order", ErrProtocol)
		}
		if !supportedItemType(event.Item.Type) || (result.itemActive && event.Item.Type != result.activeItemType) {
			return fmt.Errorf("%w: item.completed carries an unknown or changed item type", ErrProtocol)
		}
		result.itemActive = false
		result.activeItemType = ""
		result.itemCount++
	case "turn.completed":
		if result.phase != phaseInTurn || result.itemActive {
			return fmt.Errorf("%w: turn.completed is out of order", ErrProtocol)
		}
		if event.Usage.InputTokens < 0 || event.Usage.OutputTokens < 0 {
			return fmt.Errorf("%w: usage counters must be non-negative", ErrProtocol)
		}
		result.inputTokens, result.outputTokens = event.Usage.InputTokens, event.Usage.OutputTokens
		result.sawTerminal = true
		result.phase = phaseTerminal
	case "turn.failed":
		if result.phase != phaseInTurn {
			return fmt.Errorf("%w: failed turn is out of order", ErrProtocol)
		}
		result.phase = phaseTerminal
		return fmt.Errorf("%w: provider reported a terminal failure", ErrProviderFailed)
	default:
		return fmt.Errorf("%w: unknown event type", ErrProtocol)
	}
	return nil
}

// supportedItemType is the closed item-kind set observed and frozen for the
// Codex 0.145 exec JSON contract.  Unknown future item kinds require a new
// conformance receipt and adapter version instead of being silently accepted.
func supportedItemType(itemType string) bool {
	switch itemType {
	case "agent_message", "reasoning", "command_execution", "file_change", "mcp_tool_call", "web_search", "todo_list", "error":
		return true
	default:
		return false
	}
}

type streamCapture struct {
	data      []byte
	truncated bool
}

// captureStream 以固定字节上限捕获 stderr：超出部分只记录截断事实，
// 永不进入返回错误或 metadata 的自由文本。
func captureStream(reader io.Reader, limit int64) streamCapture {
	var output []byte
	buffer := make([]byte, 32<<10)
	var total int64
	truncated := false
	for {
		count, err := reader.Read(buffer)
		if count > 0 {
			total += int64(count)
			remaining := limit - int64(len(output))
			if remaining > 0 {
				take := int64(count)
				if take > remaining {
					take = remaining
				}
				output = append(output, buffer[:take]...)
			}
			if total > limit {
				truncated = true
			}
		}
		if err != nil {
			return streamCapture{data: output, truncated: truncated}
		}
	}
}
