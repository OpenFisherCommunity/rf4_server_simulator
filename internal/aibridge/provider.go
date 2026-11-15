package aibridge

// 统一 Provider 抽象：Agent 层不感知厂商，差异全部收敛到配置表。
// 首发适配器走 OpenAI 兼容协议（DeepSeek 等国内厂商可直接用），支持 function calling。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ToolSpec 是给模型看的函数定义（OpenAI function calling 形状）。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall 是模型发起的一次工具调用（内部扁平表示）。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// MarshalJSON 把内部扁平结构还原成 OpenAI 的嵌套 tool_calls 形状。
func (t ToolCall) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"id":   t.ID,
		"type": "function",
		"function": map[string]string{
			"name":      t.Name,
			"arguments": t.Arguments,
		},
	})
}

// Message 是对话消息，兼容 assistant tool_calls 与 tool 结果回传。
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Reasoning  string     `json:"reasoning_content,omitempty"`
}

// ThinkingConfig 是统一的思考开关与强度档。
type ThinkingConfig struct {
	Enabled bool
	Effort  string
}

// ChatRequest / ChatResponse 是与厂商无关的请求与响应。
type ChatRequest struct {
	Model     string
	Messages  []Message
	Tools     []ToolSpec
	Thinking  ThinkingConfig
	MaxTokens int
}

type ChatResponse struct {
	Text             string
	ToolCalls        []ToolCall
	ReasoningContent string
	RawUsage         string // 仅排障，不用于计费
}

// Provider 是所有模型厂商必须实现的接口。
type Provider interface {
	Chat(ctx context.Context, r ChatRequest) (ChatResponse, error)
	ID() string
}

// ProviderSpec 来自 ai_provider / ai_model / ai_secret 三张表。
type ProviderSpec struct {
	Name       string
	Protocol   string
	BaseURL    string
	AuthScheme string
	ModelID    string
	ConfigJSON string
	APIKey     string
}

// OpenAICompatProvider 走 /chat/completions，支持思考档位映射与工具调用。
type OpenAICompatProvider struct {
	spec        ProviderSpec
	client      *http.Client
	thinkingMap map[string]json.RawMessage
}

// NewProvider 构造 Provider。ModelID 或 APIKey 为空时返回明确错误，由上层降级。
func NewProvider(spec ProviderSpec, timeout time.Duration) (*OpenAICompatProvider, error) {
	if strings.TrimSpace(spec.BaseURL) == "" {
		return nil, errors.New("ai: base_url 未配置")
	}
	if strings.TrimSpace(spec.ModelID) == "" {
		return nil, errors.New("ai: 模型 ID 未配置，请在后台填写")
	}
	if strings.TrimSpace(spec.APIKey) == "" {
		return nil, errors.New("ai: API Key 未配置，请在后台填写")
	}
	if spec.Protocol != "" && spec.Protocol != "openai_chat" {
		return nil, fmt.Errorf("ai: 暂不支持协议 %q", spec.Protocol)
	}
	switch spec.AuthScheme {
	case "", "bearer", "x-api-key":
	default:
		return nil, fmt.Errorf("ai: 不支持的鉴权方式 %q", spec.AuthScheme)
	}
	var cfg struct {
		ThinkingMap map[string]json.RawMessage `json:"thinking_map"`
	}
	if spec.ConfigJSON != "" {
		if err := json.Unmarshal([]byte(spec.ConfigJSON), &cfg); err != nil {
			return nil, fmt.Errorf("ai: 厂商配置解析失败: %w", err)
		}
	}
	if cfg.ThinkingMap == nil {
		cfg.ThinkingMap = map[string]json.RawMessage{}
	}
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &OpenAICompatProvider{
		spec:        spec,
		client:      &http.Client{Timeout: timeout},
		thinkingMap: cfg.ThinkingMap,
	}, nil
}

func (p *OpenAICompatProvider) ID() string { return p.spec.Name }

func (p *OpenAICompatProvider) endpoint() string {
	base := strings.TrimRight(p.spec.BaseURL, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

func (p *OpenAICompatProvider) Chat(ctx context.Context, r ChatRequest) (ChatResponse, error) {
	body := map[string]any{
		"model":      p.spec.ModelID,
		"messages":   r.Messages,
		"max_tokens": r.MaxTokens,
		"stream":     false,
	}
	if len(r.Tools) > 0 {
		wire := make([]map[string]any, 0, len(r.Tools))
		for _, t := range r.Tools {
			params := t.Parameters
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			wire = append(wire, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  params,
				},
			})
		}
		body["tools"] = wire
		body["tool_choice"] = "auto"
	}
	level := r.Thinking.Effort
	if level == "" {
		level = "off"
	}
	if !r.Thinking.Enabled || len(r.Tools) > 0 {
		// 思考模式下部分厂商要求回传 reasoning_content，多轮工具调用容易踩 400；
		// 工具调用轮一律关闭思考（工具本身已给出确定性事实）。
		level = "off"
	}
	if raw, ok := p.thinkingMap[level]; ok {
		var extra map[string]json.RawMessage
		if err := json.Unmarshal(raw, &extra); err == nil {
			for k, v := range extra {
				body[k] = v
			}
		}
	} else if level == "off" {
		body["thinking"] = map[string]string{"type": "disabled"}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return ChatResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.spec.AuthScheme == "x-api-key" {
		httpReq.Header.Set("x-api-key", p.spec.APIKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+p.spec.APIKey)
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("ai: 请求厂商失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ChatResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.ToValidUTF8(string(raw), "")
		// 少数代理会把请求头或密钥回显在错误体里，先抹掉再返回。
		snippet = strings.ReplaceAll(snippet, p.spec.APIKey, "sk-***")
		if r := []rune(snippet); len(r) > 300 {
			snippet = string(r[:300])
		}
		return ChatResponse{}, fmt.Errorf("ai: 厂商返回 %d: %s", resp.StatusCode, snippet)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return ChatResponse{}, fmt.Errorf("ai: 响应解析失败: %w", err)
	}
	if len(out.Choices) == 0 {
		return ChatResponse{}, errors.New("ai: 厂商响应无 choices")
	}
	m := out.Choices[0].Message
	calls := make([]ToolCall, 0, len(m.ToolCalls))
	for _, c := range m.ToolCalls {
		calls = append(calls, ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	if len(calls) == 0 && strings.TrimSpace(m.Content) == "" {
		return ChatResponse{}, errors.New("ai: 厂商响应无文本内容")
	}
	return ChatResponse{
		Text: m.Content, ToolCalls: calls,
		ReasoningContent: m.ReasoningContent, RawUsage: string(out.Usage),
	}, nil
}
