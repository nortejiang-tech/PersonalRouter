// Package usage extracts token accounting from OpenAI Chat, OpenAI Responses and
// Anthropic JSON responses or chunked SSE streams. It only retains numeric token
// counters and the model name; response bodies are never stored or logged.
package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

const (
	// Protocol identifiers accepted by New.
	ProtocolOpenAI    = "openai"
	ProtocolResponses = "responses"
	ProtocolAnthropic = "anthropic"

	// maxJSONBytes is the largest accepted non-streaming JSON document and the
	// largest accepted single SSE data payload.
	maxJSONBytes = 2 << 20 // 2 MiB
	// maxSSEEventBytes is the largest accepted raw SSE event (all lines).
	maxSSEEventBytes = 256 << 10 // 256 KiB
)

// Result is the immutable-by-convention outcome of a single request.
type Result struct {
	InputTokens     *int64
	OutputTokens    *int64
	CacheTokens     *int64
	ReasoningTokens *int64
	Model           string
	Complete        bool
	Failed          bool
	Truncated       bool
}

// Collector accumulates usage for exactly one request. It is not safe for
// concurrent use and holds no global mutable state.
type Collector struct {
	protocol string
	stream   bool

	res Result

	// Non-streaming accumulation: chunks are buffered and parsed once at Finish.
	jsonBuf []byte

	// SSE framing state.
	eventName string
	line      []byte // current field line
	raw       []byte // raw bytes of the current event (framing only)
	data      []byte // joined data payload of the current event
	skipLine  bool   // current line is being discarded (oversized)
	skipEvent bool   // whole event is being discarded until the next blank line
	inEvent   bool
	closed    bool
}

// New returns a Collector for the given protocol ("openai", "responses" or
// "anthropic"). An unknown protocol never produces Complete.
func New(protocol string, stream bool) *Collector {
	return &Collector{protocol: strings.ToLower(strings.TrimSpace(protocol)), stream: stream}
}

// Feed supplies a chunk of the response body. Chunks may split anywhere.
// The chunk is never modified.
func (c *Collector) Feed(chunk []byte) {
	if c == nil || c.closed || len(chunk) == 0 {
		return
	}
	if c.stream {
		c.feedSSE(chunk)
		return
	}
	c.feedJSON(chunk)
}

// Finish closes the collector and returns the accumulated result. Calling it
// more than once, or feeding after it, has no further effect.
func (c *Collector) Finish() Result {
	if c == nil {
		return Result{}
	}
	if !c.closed {
		c.closed = true
		if c.stream {
			c.finishSSE()
		} else {
			c.finishJSON()
		}
	}
	// A failure always wins over completion, even when it arrives after a
	// terminal completed event.
	if c.res.Failed {
		c.res.Complete = false
	}
	// Release the buffers; only counters and the model name survive.
	c.jsonBuf = nil
	c.raw = nil
	c.data = nil
	c.line = nil
	return c.res
}

// markTruncated records truncation. Truncated results are never Complete.
func (c *Collector) markTruncated() {
	c.res.Truncated = true
	c.res.Complete = false
}

// markComplete sets Complete unless the request already failed or was truncated.
func (c *Collector) markComplete() {
	if c.res.Failed || c.res.Truncated {
		return
	}
	c.res.Complete = true
}

// markFailed records a failure and immediately clears Complete.
func (c *Collector) markFailed() {
	c.res.Failed = true
	c.res.Complete = false
}

// ---------------------------------------------------------------------------
// non-streaming JSON
// ---------------------------------------------------------------------------

func (c *Collector) feedJSON(chunk []byte) {
	// Check room before allocating so a single huge chunk cannot blow the budget.
	if len(c.jsonBuf)+len(chunk) > maxJSONBytes {
		c.jsonBuf = nil
		c.markTruncated()
		return
	}
	c.jsonBuf = append(c.jsonBuf, chunk...)
}

func (c *Collector) finishJSON() {
	if c.res.Truncated || len(c.jsonBuf) == 0 {
		return
	}
	raw, err := parseObject(string(c.jsonBuf))
	if err != nil {
		c.markFailed()
		return
	}
	if _, ok := raw["error"]; ok {
		c.markFailed()
		return
	}
	switch c.protocol {
	case ProtocolOpenAI:
		c.applyOpenAI(raw)
	case ProtocolResponses:
		c.applyResponses(raw)
	case ProtocolAnthropic:
		c.applyAnthropic(raw)
	}
}

// ---------------------------------------------------------------------------
// SSE framing
// ---------------------------------------------------------------------------

func (c *Collector) feedSSE(chunk []byte) {
	for _, b := range chunk {
		c.feedByte(b)
	}
}

func (c *Collector) feedByte(b byte) {
	if b == '\n' {
		if c.skipLine {
			c.skipLine = false
			c.line = c.line[:0]
			return
		}
		if len(c.line) > 0 && c.line[len(c.line)-1] == '\r' {
			// CRLF is one line ending; remove CR before deciding whether
			// this is the blank line that dispatches an event.
			c.line = c.line[:len(c.line)-1]
		}
		if len(c.line) == 0 {
			// Blank line: the only thing that dispatches an event.
			if c.skipEvent {
				c.skipEvent = false
				c.raw = c.raw[:0]
				c.data = c.data[:0]
			}
			if c.inEvent {
				c.flushEvent()
			}
			c.eventName = ""
			return
		}
		c.endLine()
		return
	}
	if c.skipLine {
		return
	}
	if len(c.line) >= maxSSEEventBytes {
		// Bound the line before appending another byte. The entire event is
		// discarded through its next blank line, not merely this field.
		c.markTruncated()
		c.skipLine = true
		c.skipEvent = true
		c.line = c.line[:0]
		return
	}
	c.line = append(c.line, b)
}

// endLine incorporates the completed line into the current event.
func (c *Collector) endLine() {
	line := c.line
	c.line = c.line[:0]
	if c.skipEvent {
		return
	}
	// The raw event keeps its LF-normalised lines so the size limit covers the
	// whole event, not just one field.
	need := len(line) + 1
	if len(c.raw)+need > maxSSEEventBytes {
		// Drop everything from the oversized line through the next blank line.
		c.markTruncated()
		c.raw = c.raw[:0]
		c.data = c.data[:0]
		c.skipEvent = true
		return
	}
	c.raw = append(c.raw, line...)
	c.raw = append(c.raw, '\n')
	c.handleLine(line)
}

func (c *Collector) handleLine(rawLine []byte) {
	if len(rawLine) == 0 {
		return
	}
	c.inEvent = true
	field, value := splitField(rawLine)
	switch field {
	case "event":
		c.eventName = value
	case "data":
		need := len(value) + 1
		if len(c.data)+need > maxJSONBytes || len(c.data)+need > maxSSEEventBytes {
			// The event payload cannot be parsed anyway; drop the rest of the
			// event and keep the truncated flag for later events.
			c.markTruncated()
			c.raw = c.raw[:0]
			c.data = c.data[:0]
			c.skipEvent = true
			return
		}
		c.data = append(c.data, value...)
		c.data = append(c.data, '\n')
	}
	// Unknown fields (id, retry, comments) are kept for sizing but ignored.
}

func splitField(line []byte) (field, value string) {
	i := bytes.IndexByte(line, ':')
	if i < 0 {
		return string(line), ""
	}
	field = string(line[:i])
	value = string(line[i+1:])
	value = strings.TrimPrefix(value, " ")
	value = strings.TrimPrefix(value, "\t")
	return field, value
}

func (c *Collector) flushEvent() {
	data := strings.TrimSuffix(string(c.data), "\n")
	name := c.eventName
	c.raw = c.raw[:0]
	c.data = c.data[:0]
	c.inEvent = false
	c.eventName = ""
	c.handleEvent(name, data)
}

func (c *Collector) finishSSE() {
	if !c.skipEvent && !c.skipLine && len(c.line) > 0 {
		if c.line[len(c.line)-1] == '\r' {
			c.line = c.line[:len(c.line)-1]
		}
		c.handleLine(c.line)
	}
	c.line = nil
	// An event that never reached its terminating blank line is incomplete and
	// is deliberately dropped rather than dispatched.
	c.inEvent = false
	c.eventName = ""
}

func (c *Collector) handleEvent(name, data string) {
	if c.res.Failed {
		return
	}
	// Comment/heartbeat events and blank data fields carry no protocol payload.
	// They are valid SSE framing and must not become parse failures.
	if strings.TrimSpace(data) == "" {
		return
	}
	switch c.protocol {
	case ProtocolOpenAI:
		if strings.TrimSpace(data) == "[DONE]" {
			c.markComplete()
			return
		}
		raw, err := parseObject(data)
		if err != nil {
			c.markFailed()
			return
		}
		if _, ok := raw["error"]; ok {
			c.markFailed()
			return
		}
		// A streamed Chat chunk contributes usage/model metadata, but only
		// [DONE] is a successful terminal signal.
		c.applyOpenAIUsage(raw)
		c.applyModel(raw)
	case ProtocolResponses:
		c.handleResponsesEvent(name, data)
	case ProtocolAnthropic:
		c.handleAnthropicEvent(name, data)
	}
}

func (c *Collector) handleResponsesEvent(name, data string) {
	raw, err := parseObject(data)
	if err != nil {
		c.markFailed()
		return
	}
	if _, ok := raw["error"]; ok {
		c.markFailed()
		return
	}
	typ := name
	if t, ok := stringField(raw, "type"); ok && t != "" {
		typ = t
	}
	if resp, ok := objField(raw, "response"); ok {
		if st, ok := stringField(resp, "status"); ok && st == "failed" {
			c.markFailed()
			return
		}
	}
	switch typ {
	case "response.completed":
		resp, ok := objField(raw, "response")
		if !ok {
			return
		}
		if st, ok := stringField(resp, "status"); ok && st != "completed" {
			if st == "failed" {
				c.markFailed()
			}
			return
		}
		c.applyResponsesUsage(resp)
		c.applyModel(resp)
		c.markComplete()
	case "response.failed":
		c.markFailed()
	case "response.incomplete":
		// terminal but not a successful completion
	case "response":
		// A bare "response" type is not a completion signal on its own.
		c.applyResponsesUsage(raw)
		c.applyModel(raw)
		if st, ok := stringField(raw, "status"); ok && st == "completed" {
			c.markComplete()
		}
	default:
		// intermediate / unknown event: harvest counters only, never complete.
		c.applyResponsesUsage(raw)
		c.applyModel(raw)
	}
}

func (c *Collector) handleAnthropicEvent(name, data string) {
	raw, err := parseObject(data)
	if err != nil {
		c.markFailed()
		return
	}
	if _, ok := raw["error"]; ok {
		c.markFailed()
		return
	}
	typ := name
	if t, ok := stringField(raw, "type"); ok && t != "" {
		typ = t
	}
	switch typ {
	case "message_start":
		msg, ok := objField(raw, "message")
		if !ok {
			return
		}
		c.applyAnthropicUsage(msg)
		c.applyModel(msg)
	case "message_delta":
		// cumulative output_tokens: replace, never add.
		if u, ok := objField(raw, "usage"); ok {
			if v, ok := int64Field(u, "output_tokens"); ok {
				c.res.OutputTokens = &v
			}
		}
	case "message_stop":
		c.markComplete()
	case "error":
		c.markFailed()
	}
}

// ---------------------------------------------------------------------------
// protocol field extraction
// ---------------------------------------------------------------------------

func (c *Collector) applyOpenAI(raw map[string]json.RawMessage) {
	c.applyOpenAIUsage(raw)
	c.applyModel(raw)
	// A completion must look like a Chat completion: at least a choices array or
	// a usage object. Bare {} / unrelated JSON is not evidence.
	_, hasChoices := arrayField(raw, "choices")
	if !hasChoices {
		if _, hasUsage := objField(raw, "usage"); !hasUsage {
			return
		}
	}
	c.markComplete()
}

func (c *Collector) applyOpenAIUsage(raw map[string]json.RawMessage) {
	u, ok := objField(raw, "usage")
	if !ok {
		return
	}
	if v, ok := int64Field(u, "prompt_tokens"); ok {
		c.res.InputTokens = &v
	}
	if v, ok := int64Field(u, "completion_tokens"); ok {
		c.res.OutputTokens = &v
	}
	if d, ok := objField(u, "prompt_tokens_details"); ok {
		if v, ok := int64Field(d, "cached_tokens"); ok {
			c.res.CacheTokens = &v
		}
	}
	if d, ok := objField(u, "completion_tokens_details"); ok {
		if v, ok := int64Field(d, "reasoning_tokens"); ok {
			c.res.ReasoningTokens = &v
		}
	}
}

func (c *Collector) applyResponses(raw map[string]json.RawMessage) {
	c.applyResponsesUsage(raw)
	c.applyModel(raw)
	if c.res.Failed {
		return
	}
	st, ok := stringField(raw, "status")
	if !ok || st != "completed" {
		// queued / in_progress / incomplete / missing never complete.
		if st == "failed" {
			c.markFailed()
		}
		return
	}
	c.markComplete()
}

func (c *Collector) applyResponsesUsage(raw map[string]json.RawMessage) {
	u, ok := objField(raw, "usage")
	if !ok {
		return
	}
	if v, ok := int64Field(u, "input_tokens"); ok {
		c.res.InputTokens = &v
	}
	if v, ok := int64Field(u, "output_tokens"); ok {
		c.res.OutputTokens = &v
	}
	if d, ok := objField(u, "input_tokens_details"); ok {
		if v, ok := int64Field(d, "cached_tokens"); ok {
			c.res.CacheTokens = &v
		}
	}
	if d, ok := objField(u, "output_tokens_details"); ok {
		if v, ok := int64Field(d, "reasoning_tokens"); ok {
			c.res.ReasoningTokens = &v
		}
	}
}

func (c *Collector) applyAnthropic(raw map[string]json.RawMessage) {
	c.applyAnthropicUsage(raw)
	c.applyModel(raw)
	// A completion must look like an Anthropic message.
	typ, hasType := stringField(raw, "type")
	if typ == "error" {
		c.markFailed()
		return
	}
	if !hasType && !hasUsageObject(raw) {
		return
	}
	if hasType && typ != "message" && !hasUsageObject(raw) {
		return
	}
	c.markComplete()
}

func hasUsageObject(raw map[string]json.RawMessage) bool {
	_, ok := objField(raw, "usage")
	return ok
}

func (c *Collector) applyAnthropicUsage(raw map[string]json.RawMessage) {
	u, ok := objField(raw, "usage")
	if !ok {
		return
	}
	if v, ok := int64Field(u, "input_tokens"); ok {
		c.res.InputTokens = &v
	}
	if v, ok := int64Field(u, "output_tokens"); ok {
		c.res.OutputTokens = &v
	}
	// Cache counts reads only; cache_creation_* tokens are deliberately excluded.
	if v, ok := int64Field(u, "cache_read_input_tokens"); ok {
		c.res.CacheTokens = &v
	}
}

func (c *Collector) applyModel(raw map[string]json.RawMessage) {
	if m, ok := stringField(raw, "model"); ok && m != "" {
		c.res.Model = m
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func parseObject(s string) (map[string]json.RawMessage, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil, errors.New("empty")
	}
	if len(trimmed) > maxJSONBytes {
		return nil, errors.New("too large")
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return nil, err
	}
	if out == nil {
		// JSON literal null is not a usable object.
		return nil, errors.New("null")
	}
	return out, nil
}

func stringField(raw map[string]json.RawMessage, key string) (string, bool) {
	v, ok := raw[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		return "", false
	}
	return s, true
}

func objField(raw map[string]json.RawMessage, key string) (map[string]json.RawMessage, bool) {
	v, ok := raw[key]
	if !ok {
		return nil, false
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(v, &out); err != nil {
		return nil, false
	}
	if out == nil {
		return nil, false
	}
	return out, true
}

func arrayField(raw map[string]json.RawMessage, key string) ([]json.RawMessage, bool) {
	v, ok := raw[key]
	if !ok {
		return nil, false
	}
	var out []json.RawMessage
	if err := json.Unmarshal(v, &out); err != nil || out == nil {
		return nil, false
	}
	return out, true
}

// int64Field returns the value only when it is a non-negative int64. Negative,
// fractional and overflowing values are rejected (left untouched).
func int64Field(raw map[string]json.RawMessage, key string) (int64, bool) {
	v, ok := raw[key]
	if !ok {
		return 0, false
	}
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.UseNumber()
	var n interface{}
	if err := dec.Decode(&n); err != nil {
		return 0, false
	}
	num, ok := n.(json.Number)
	if !ok {
		return 0, false
	}
	s := num.String()
	if strings.ContainsAny(s, ".eE") {
		return 0, false
	}
	parsed, err := strconv.ParseInt(s, 10, 64)
	if err != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}
