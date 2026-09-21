package usage

import (
	"fmt"
	"strings"
	"testing"
)

func iv(v int64) *int64 { return &v }

func check(t *testing.T, label string, r Result, in, out, cache, reason *int64, model string) {
	t.Helper()
	assertField(t, label+" input", r.InputTokens, in)
	assertField(t, label+" output", r.OutputTokens, out)
	assertField(t, label+" cache", r.CacheTokens, cache)
	assertField(t, label+" reasoning", r.ReasoningTokens, reason)
	if r.Model != model {
		t.Errorf("%s: model = %q, want %q", label, r.Model, model)
	}
}

func assertField(t *testing.T, label string, got, want *int64) {
	t.Helper()
	switch {
	case want == nil && got != nil:
		t.Errorf("%s: got %d, want nil", label, *got)
	case want != nil && got == nil:
		t.Errorf("%s: got nil, want %d", label, *want)
	case want != nil && got != nil && *want != *got:
		t.Errorf("%s: got %d, want %d", label, *got, *want)
	}
}

// 1
func TestOpenAIJSONFull(t *testing.T) {
	body := `{"model":"gpt-5-mini","usage":{"prompt_tokens":120,"completion_tokens":340,
	  "prompt_tokens_details":{"cached_tokens":80},
	  "completion_tokens_details":{"reasoning_tokens":210}}}`
	c := New("openai", false)
	c.Feed([]byte(body))
	r := c.Finish()
	check(t, "openai json", r, iv(120), iv(340), iv(80), iv(210), "gpt-5-mini")
	if !r.Complete || r.Failed || r.Truncated {
		t.Errorf("flags = %+v, want complete only", r)
	}
}

// 2
func TestOpenAIJSONMissingUsageKeepsNil(t *testing.T) {
	c := New("openai", false)
	c.Feed([]byte(`{"model":"gpt-5","choices":[{"index":0}]}`))
	r := c.Finish()
	check(t, "missing usage", r, nil, nil, nil, nil, "gpt-5")
	if !r.Complete {
		t.Error("valid object should be complete")
	}
}

// 3
func TestZeroVersusNil(t *testing.T) {
	c := New("anthropic", false)
	c.Feed([]byte(`{"model":"claude","usage":{"input_tokens":0,"output_tokens":5}}`))
	r := c.Finish()
	check(t, "zero vs nil", r, iv(0), iv(5), nil, nil, "claude")

	c2 := New("anthropic", false)
	c2.Feed([]byte(`{"model":"claude"}`))
	r2 := c2.Finish()
	check(t, "all nil", r2, nil, nil, nil, nil, "claude")
}

// 4
func TestRejectNegativeFractionalOverflow(t *testing.T) {
	cases := []string{
		`{"model":"m","usage":{"prompt_tokens":-5,"completion_tokens":2}}`,
		`{"model":"m","usage":{"prompt_tokens":1.5,"completion_tokens":2}}`,
		`{"model":"m","usage":{"prompt_tokens":99999999999999999999,"completion_tokens":2}}`,
	}
	for i, body := range cases {
		c := New("openai", false)
		c.Feed([]byte(body))
		r := c.Finish()
		if r.InputTokens != nil {
			t.Errorf("case %d: input = %d, want nil (rejected)", i, *r.InputTokens)
		}
		check(t, fmt.Sprintf("case %d out", i), r, nil, iv(2), nil, nil, "m")
	}
}

// 5
func TestJSONErrorFieldFails(t *testing.T) {
	c := New("openai", false)
	c.Feed([]byte(`{"error":{"message":"bad key"}}`))
	r := c.Finish()
	if !r.Failed || r.Complete {
		t.Errorf("flags = failed:%v complete:%v, want failed only", r.Failed, r.Complete)
	}
}

// 6
func TestMalformedJSONNotComplete(t *testing.T) {
	for _, body := range []string{`{`, `not json`, `[1,2]`, `""`, ``} {
		c := New("openai", false)
		c.Feed([]byte(body))
		if r := c.Finish(); r.Complete {
			t.Errorf("body %q should not be complete", body)
		}
	}
}

// 7
func TestResponsesJSONStatuses(t *testing.T) {
	c := New("responses", false)
	c.Feed([]byte(`{"status":"completed","model":"r1","usage":{"input_tokens":10,"output_tokens":20,
	  "input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":7}}}`))
	r := c.Finish()
	check(t, "responses completed", r, iv(10), iv(20), iv(4), iv(7), "r1")
	if !r.Complete || r.Failed {
		t.Errorf("completed flags wrong: %+v", r)
	}

	c = New("responses", false)
	c.Feed([]byte(`{"status":"failed"}`))
	if r := c.Finish(); !r.Failed || r.Complete {
		t.Errorf("failed status: %+v", r)
	}

	c = New("responses", false)
	c.Feed([]byte(`{"status":"incomplete","usage":{"input_tokens":3}}`))
	if r := c.Finish(); r.Complete || r.Failed {
		t.Errorf("incomplete status: %+v", r)
	}
}

// 8
func TestAnthropicCacheExcludesCreation(t *testing.T) {
	c := New("anthropic", false)
	c.Feed([]byte(`{"model":"claude-x","usage":{"input_tokens":9,"output_tokens":11,
	  "cache_creation_input_tokens":99,"cache_read_input_tokens":7}}`))
	r := c.Finish()
	check(t, "anthropic", r, iv(9), iv(11), iv(7), nil, "claude-x")
}

// 9
func TestUnknownProtocolNeverComplete(t *testing.T) {
	for _, p := range []string{"gemini", "", "OPENAI-X"} {
		c := New(p, false)
		c.Feed([]byte(`{"model":"m","usage":{"input_tokens":1}}`))
		if r := c.Finish(); r.Complete {
			t.Errorf("protocol %q must not complete", p)
		}
	}
}

// 10
func TestOpenAISSEDoneAndUsage(t *testing.T) {
	stream := "data: {\"model\":\"gpt-5\",\"choices\":[]}\n\n" +
		"data: {\"model\":\"gpt-5\",\"usage\":{\"prompt_tokens\":50,\"completion_tokens\":60," +
		"\"completion_tokens_details\":{\"reasoning_tokens\":40}}}\n\n" +
		"data: [DONE]\n\n"
	c := New("openai", true)
	c.Feed([]byte(stream))
	r := c.Finish()
	check(t, "openai sse", r, iv(50), iv(60), nil, iv(40), "gpt-5")
	if !r.Complete {
		t.Error("want complete after [DONE]")
	}
}

// 11
func TestSSESingleByteFeeding(t *testing.T) {
	stream := ": keep-alive\r\n\r\n" +
		"event: message_start\r\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-y\"," +
		"\"usage\":{\"input_tokens\":30,\"cache_read_input_tokens\":6}}}\r\n\r\n" +
		"event: message_delta\r\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":15}}\r\n\r\n" +
		"event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n"
	c := New("anthropic", true)
	for i := 0; i < len(stream); i++ {
		c.Feed([]byte(stream[i : i+1]))
	}
	r := c.Finish()
	check(t, "anthropic byte-wise", r, iv(30), iv(15), iv(6), nil, "claude-y")
	if !r.Complete || r.Failed {
		t.Errorf("flags = %+v", r)
	}
}

// 12
func TestAnthropicCumulativeReplace(t *testing.T) {
	// message_delta carries cumulative output_tokens; must replace, not sum.
	stream := "data: {\"type\":\"message_start\",\"message\":{\"model\":\"c\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":10}}\n\n" +
		"data: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":25}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	c := New("anthropic", true)
	c.Feed([]byte(stream))
	r := c.Finish()
	check(t, "cumulative", r, iv(1), iv(25), nil, nil, "c")
}

// 13
func TestResponsesSSEEvents(t *testing.T) {
	stream := "event: response.created\ndata: {\"type\":\"response.created\"}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"r2\"," +
		"\"usage\":{\"input_tokens\":7,\"output_tokens\":9}}}\n\n"
	c := New("responses", true)
	c.Feed([]byte(stream))
	r := c.Finish()
	check(t, "responses sse", r, iv(7), iv(9), nil, nil, "r2")
	if !r.Complete {
		t.Error("want complete")
	}

	c = New("responses", true)
	c.Feed([]byte("event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n"))
	if r := c.Finish(); !r.Failed || r.Complete {
		t.Errorf("response.failed: %+v", r)
	}

	c = New("responses", true)
	c.Feed([]byte("event: response.incomplete\ndata: {\"type\":\"response.incomplete\"}\n\n"))
	if r := c.Finish(); r.Complete || r.Failed {
		t.Errorf("response.incomplete: %+v", r)
	}
}

// 14
func TestFailedStickyAcrossEvents(t *testing.T) {
	stream := "event: error\ndata: {\"type\":\"error\",\"error\":{\"msg\":\"boom\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	c := New("anthropic", true)
	c.Feed([]byte(stream))
	r := c.Finish()
	if !r.Failed || r.Complete {
		t.Errorf("failed must stick: failed=%v complete=%v", r.Failed, r.Complete)
	}
}

// 15
func TestIncompleteSSENotComplete(t *testing.T) {
	c := New("anthropic", true)
	c.Feed([]byte("data: {\"type\":\"message_start\",\"message\":{\"model\":\"c\"}}\n\n"))
	c.Feed([]byte("data: {\"type\":\"mess"))
	if r := c.Finish(); r.Complete {
		t.Error("unterminated stream must not complete")
	}
}

// 16
func TestMultilineDataEvent(t *testing.T) {
	// Two data lines in one event are joined with \n; the joined payload is parsed.
	stream := "event: message_delta\ndata: {\"type\":\"message_delta\",\ndata: \"usage\":{\"output_tokens\":42}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
	c := New("anthropic", true)
	c.Feed([]byte(stream))
	r := c.Finish()
	check(t, "multiline", r, nil, iv(42), nil, nil, "")
	if !r.Complete {
		t.Error("want complete")
	}
}

// 17
func TestSSEEventSizeLimit(t *testing.T) {
	big := strings.Repeat("a", maxSSEEventBytes+10)
	stream := "data: " + big + "\n\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"model\":\"c\",\"usage\":{\"input_tokens\":1}}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	c := New("anthropic", true)
	c.Feed([]byte(stream))
	r := c.Finish()
	if !r.Truncated {
		t.Fatal("oversized event must set Truncated")
	}
	if r.Complete {
		t.Error("Truncated must not be cleared by later terminal event")
	}
	// later events still parsed
	check(t, "after truncation", r, iv(1), nil, nil, nil, "c")
}

// 18
func TestJSONSizeLimit(t *testing.T) {
	big := `{"model":"m","pad":"` + strings.Repeat("x", maxJSONBytes) + `"}`
	c := New("openai", false)
	c.Feed([]byte(big))
	r := c.Finish()
	if !r.Truncated || r.Complete {
		t.Errorf("oversized json: truncated=%v complete=%v", r.Truncated, r.Complete)
	}
}

// 19
func TestFinishIdempotent(t *testing.T) {
	c := New("openai", false)
	c.Feed([]byte(`{"model":"m","usage":{"prompt_tokens":3}}`))
	a := c.Finish()
	b := c.Finish()
	c.Feed([]byte(`{"model":"n","usage":{"prompt_tokens":9}}`))
	d := c.Finish()
	if a.Complete != b.Complete || *a.InputTokens != *b.InputTokens || a.Model != b.Model {
		t.Errorf("second Finish differs: %+v vs %+v", a, b)
	}
	if *d.InputTokens != 3 || d.Model != "m" {
		t.Errorf("Feed after Finish changed result: %+v", d)
	}
}

// 20
func TestFeedDoesNotMutateInput(t *testing.T) {
	body := []byte(`{"model":"gpt-5","usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	orig := append([]byte(nil), body...)
	c := New("openai", false)
	c.Feed(body)
	_ = c.Finish()
	if string(body) != string(orig) {
		t.Error("Feed mutated caller slice")
	}

	sse := []byte("data: {\"type\":\"message_stop\"}\n\n")
	origSSE := append([]byte(nil), sse...)
	c2 := New("anthropic", true)
	for i := 0; i < len(sse); i += 3 {
		end := i + 3
		if end > len(sse) {
			end = len(sse)
		}
		c2.Feed(sse[i:end])
	}
	_ = c2.Finish()
	if string(sse) != string(origSSE) {
		t.Error("streaming Feed mutated caller slice")
	}
}

// 21
func TestProtocolCaseInsensitive(t *testing.T) {
	c := New("  Anthropic ", false)
	c.Feed([]byte(`{"model":"c","usage":{"input_tokens":4}}`))
	if r := c.Finish(); !r.Complete || *r.InputTokens != 4 {
		t.Errorf("protocol normalization failed: %+v", r)
	}
}

// 22 - defect 1: non-streaming must accumulate every Feed segment and parse the
// complete document exactly once at Finish, regardless of segment sizes.
func TestJSONAccumulatesArbitrarySegments(t *testing.T) {
	body := `{"model":"gpt-5","usage":{"prompt_tokens":11,"completion_tokens":22,` +
		`"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":9}}}`
	c := New("openai", false)
	for i := 0; i < len(body); i++ {
		c.Feed([]byte(body[i : i+1]))
	}
	r := c.Finish()
	check(t, "byte-wise json", r, iv(11), iv(22), iv(3), iv(9), "gpt-5")
	if !r.Complete || r.Failed || r.Truncated {
		t.Errorf("flags = %+v, want complete only", r)
	}
	// Parsing happens at Finish, not during Feed: a document that is only valid
	// once complete must not complete before Finish.
	c2 := New("openai", false)
	c2.Feed([]byte(`{"model":"m","usage":{"prompt_tokens":7,"completion_tokens":1}}`))
	if r := c2.Finish(); !r.Complete || r.InputTokens == nil || *r.InputTokens != 7 {
		t.Errorf("late-parsing contract broken: %+v", r)
	}
}

// 23 - defect 1: the limit is cumulative, not per chunk. Each chunk is small,
// the total exceeds 2 MiB, so the buffer is dropped and Truncated is set.
func TestJSONCumulativeLimitNotPerChunk(t *testing.T) {
	pad := strings.Repeat("x", 4096)
	body := `{"model":"m","usage":{"prompt_tokens":1,"completion_tokens":2},"pad":"` + pad + `"}`
	c := New("openai", false)
	for i := 0; i < len(body); i += 512 { // every chunk far below the limit
		end := i + 512
		if end > len(body) {
			end = len(body)
		}
		c.Feed([]byte(body[i:end]))
	}
	// Push the cumulative total over maxJSONBytes with one small chunk.
	over := maxJSONBytes - len(body) + 1
	for i := 0; i < over; i += 128 {
		n := 128
		if over-i < n {
			n = over - i
		}
		c.Feed([]byte(strings.Repeat("y", n)))
	}
	r := c.Finish()
	if !r.Truncated || r.Complete {
		t.Errorf("cumulative overflow: truncated=%v complete=%v", r.Truncated, r.Complete)
	}
	check(t, "cumulative overflow keeps no counters", r, nil, nil, nil, nil, "")
}

// 24 - defect 1: a single oversized chunk is also rejected without allocating it
// into the buffer, and feeding after truncation cannot resurrect the result.
func TestJSONOversizedChunkAndPostTruncationFeed(t *testing.T) {
	big := `{"model":"m","usage":{"prompt_tokens":1}}` + strings.Repeat("z", maxJSONBytes)
	c := New("openai", false)
	c.Feed([]byte(big))
	c.Feed([]byte(`{"model":"m","usage":{"prompt_tokens":1}}`))
	r := c.Finish()
	if !r.Truncated || r.Complete {
		t.Errorf("oversized chunk: truncated=%v complete=%v", r.Truncated, r.Complete)
	}
	check(t, "oversized chunk", r, nil, nil, nil, nil, "")
}

// 25 - defect 2: CRLF is only a line ending. A named event followed by several
// CRLF terminated data lines must join into one payload and dispatch on the blank
// line only.
func TestSSECRLFMultilineData(t *testing.T) {
	stream := "event: message_delta\r\n" +
		"data: {\"type\":\"message_delta\",\r\n" +
		"data: \"usage\":{\"output_tokens\":42}}\r\n" +
		"\r\n" +
		"event: message_stop\r\n" +
		"data: {\"type\":\"message_stop\"}\r\n" +
		"\r\n"
	c := New("anthropic", true)
	c.Feed([]byte(stream))
	r := c.Finish()
	check(t, "crlf multiline", r, nil, iv(42), nil, nil, "")
	if !r.Complete || r.Failed || r.Truncated {
		t.Errorf("flags = %+v, want complete only", r)
	}
}

// 26 - defect 2: mixed LF/CRLF framing fed in awkward 7-byte segments, with
// comment lines that must be ignored.
func TestSSEMixedLineEndingsSegmented(t *testing.T) {
	stream := ": keep-alive\n\n" +
		"event: message_start\r\ndata: {\"type\":\"message_start\",\n" +
		"data: \"message\":{\"model\":\"c\",\"usage\":{\"input_tokens\":8}}}\r\n\r\n" +
		"# ignored comment\r\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	c := New("anthropic", true)
	for i := 0; i < len(stream); i += 7 {
		end := i + 7
		if end > len(stream) {
			end = len(stream)
		}
		c.Feed([]byte(stream[i:end]))
	}
	r := c.Finish()
	check(t, "mixed endings", r, iv(8), nil, nil, nil, "c")
	if !r.Complete || r.Failed || r.Truncated {
		t.Errorf("flags = %+v, want complete only", r)
	}
}

// 27 - defect 2: at EOF an event that never reached its terminating blank line
// is incomplete and must not be dispatched as Complete.
func TestSSEEOFWithoutBlankLineNotComplete(t *testing.T) {
	for _, stream := range []string{
		"event: message_stop\ndata: {\"type\":\"message_stop\"}",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n",
		"event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n",
	} {
		c := New("anthropic", true)
		c.Feed([]byte(stream))
		if r := c.Finish(); r.Complete {
			t.Errorf("stream %q must not complete without a blank line", stream)
		}
	}
}

// 28 - defect 3: an oversized event must be dropped through the next blank line.
// Its remaining data lines must not be reparsed as a fresh valid event, and a
// later independent event is still parsed while Truncated stays set.
func TestSSEOversizedEventDroppedUntilBlankLine(t *testing.T) {
	big := strings.Repeat("a", maxSSEEventBytes+10)
	stream := "event: message_start\ndata: {\"type\":\"message_start\",\ndata: \"x\":" + big + "}\n" +
		"data: {\"type\":\"message_stop\"}\n\n" +
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"c\",\"usage\":{\"input_tokens\":5}}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	c := New("anthropic", true)
	c.Feed([]byte(stream))
	r := c.Finish()
	if !r.Truncated {
		t.Fatal("oversized event must set Truncated")
	}
	if r.Complete {
		t.Error("Truncated must not be cleared by a later terminal event")
	}
	// The tail of the oversized event must not have been mistaken for a real
	// message_stop; only the later independent events may contribute.
	check(t, "after oversized event", r, iv(5), nil, nil, nil, "c")
}

// 29 - defect 3: many small lines whose joined data payload exceeds the event
// budget are dropped as one event, not accepted line by line.
func TestSSEOversizedJoinedDataPayload(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("event: message_start\n")
	for i := 0; i < 300; i++ {
		sb.WriteString("data: ")
		sb.WriteString(strings.Repeat("b", 1024))
		sb.WriteString("\n")
	}
	sb.WriteString("data: {\"type\":\"message_stop\"}\n\n")
	c := New("anthropic", true)
	c.Feed([]byte(sb.String()))
	r := c.Finish()
	if !r.Truncated || r.Complete {
		t.Errorf("joined payload overflow: truncated=%v complete=%v", r.Truncated, r.Complete)
	}
}

// 30 - defect 4: a failure after a completed/[DONE] event must clear Complete.
func TestFailedClearsCompleteAfterSuccess(t *testing.T) {
	c := New("anthropic", true)
	c.Feed([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	c.Feed([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"msg\":\"late boom\"}}\n\n"))
	if r := c.Finish(); !r.Failed || r.Complete {
		t.Errorf("late error: failed=%v complete=%v", r.Failed, r.Complete)
	}

	c = New("openai", true)
	c.Feed([]byte("data: {\"model\":\"gpt-5\",\"usage\":{\"prompt_tokens\":1}}\n\ndata: [DONE]\n\n"))
	c.Feed([]byte("data: {\"error\":{\"message\":\"late\"}}\n\n"))
	if r := c.Finish(); !r.Failed || r.Complete {
		t.Errorf("openai late error: failed=%v complete=%v", r.Failed, r.Complete)
	}

	c = New("responses", true)
	c.Feed([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":2}}}\n\n"))
	c.Feed([]byte("event: response.failed\ndata: {\"type\":\"response.failed\"}\n\n"))
	r := c.Finish()
	if !r.Failed || r.Complete {
		t.Errorf("late response.failed: failed=%v complete=%v", r.Failed, r.Complete)
	}
	if r.InputTokens == nil || *r.InputTokens != 2 {
		t.Errorf("counters harvested before the failure must survive")
	}
}

// 31 - defect 4: Truncated set after a completion also clears Complete.
func TestTruncatedClearsCompleteAfterSuccess(t *testing.T) {
	c := New("anthropic", true)
	c.Feed([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	big := strings.Repeat("q", maxSSEEventBytes+1)
	c.Feed([]byte("data: " + big + "\n\n"))
	if r := c.Finish(); !r.Truncated || r.Complete {
		t.Errorf("truncation after success: truncated=%v complete=%v", r.Truncated, r.Complete)
	}
}

// 32 - defect 4: non-streaming Responses only completes on status "completed".
func TestResponsesJSONOnlyCompletedCompletes(t *testing.T) {
	for _, st := range []string{"queued", "in_progress", "incomplete"} {
		c := New("responses", false)
		c.Feed([]byte(`{"status":"` + st + `","model":"r","usage":{"input_tokens":3,"output_tokens":4}}`))
		r := c.Finish()
		if r.Complete || r.Failed {
			t.Errorf("status %q: complete=%v failed=%v, want neither", st, r.Complete, r.Failed)
		}
		check(t, "status "+st, r, iv(3), iv(4), nil, nil, "r")
	}
	// missing status is not a completion either
	c := New("responses", false)
	c.Feed([]byte(`{"model":"r","usage":{"input_tokens":3}}`))
	if r := c.Finish(); r.Complete || r.Failed {
		t.Errorf("missing status: complete=%v failed=%v", r.Complete, r.Failed)
	}
	// explicit completed does complete
	c = New("responses", false)
	c.Feed([]byte(`{"status":"completed","model":"r","usage":{"input_tokens":3}}`))
	if r := c.Finish(); !r.Complete || r.Failed {
		t.Errorf("completed status: %+v", r)
	}
}

// 33 - defect 4: SSE type "response" is not an unconditional completion, and
// response.completed requires a usable response object.
func TestResponsesSSECompletionRequiresEvidence(t *testing.T) {
	c := New("responses", true)
	c.Feed([]byte("event: response\ndata: {\"type\":\"response\",\"status\":\"in_progress\",\"usage\":{\"input_tokens\":6}}\n\n"))
	if r := c.Finish(); r.Complete {
		t.Error("type=response with in_progress must not complete")
	}

	c = New("responses", true)
	c.Feed([]byte("event: response\ndata: {\"type\":\"response\",\"status\":\"completed\",\"usage\":{\"input_tokens\":6}}\n\n"))
	if r := c.Finish(); !r.Complete || r.Failed {
		t.Errorf("type=response with completed: %+v", r)
	}

	c = New("responses", true)
	c.Feed([]byte("event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"))
	if r := c.Finish(); r.Complete {
		t.Error("response.completed without a response object must not complete")
	}

	c = New("responses", true)
	c.Feed([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"failed\"}}\n\n"))
	if r := c.Finish(); !r.Failed || r.Complete {
		t.Errorf("nested failed response: failed=%v complete=%v", r.Failed, r.Complete)
	}

	c = New("responses", true)
	c.Feed([]byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"incomplete\"}}\n\n"))
	if r := c.Finish(); r.Complete || r.Failed {
		t.Errorf("nested incomplete response: %+v", r)
	}
}

// 34 - defect 4: JSON null and {} are never treated as a valid completion.
func TestNullAndEmptyObjectNotComplete(t *testing.T) {
	for _, body := range []string{"null", "{}", "  \n{}\n  ", `{"error":null}`} {
		for _, p := range []string{ProtocolOpenAI, ProtocolResponses, ProtocolAnthropic} {
			c := New(p, false)
			c.Feed([]byte(body))
			r := c.Finish()
			if r.Complete {
				t.Errorf("protocol %s body %q must not complete", p, body)
			}
			if strings.Contains(body, "error") && !r.Failed {
				t.Errorf("protocol %s body %q must fail", p, body)
			}
		}
	}
	// Same through SSE framing.
	for _, p := range []string{ProtocolOpenAI, ProtocolResponses, ProtocolAnthropic} {
		c := New(p, true)
		c.Feed([]byte("data: null\n\ndata: {}\n\n"))
		if r := c.Finish(); r.Complete {
			t.Errorf("protocol %s sse null/{} must not complete", p)
		}
	}
}

// 35 - defect 4: an explicit error event fails for every protocol, including
// when it arrives before any successful event.
func TestExplicitErrorEventFails(t *testing.T) {
	c := New("responses", true)
	c.Feed([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"nope\"}}\n\n"))
	if r := c.Finish(); !r.Failed || r.Complete {
		t.Errorf("responses error event: %+v", r)
	}

	c = New("openai", true)
	c.Feed([]byte("data: {\"error\":{\"message\":\"nope\"}}\n\ndata: [DONE]\n\n"))
	if r := c.Finish(); !r.Failed || r.Complete {
		t.Errorf("openai error then [DONE]: %+v", r)
	}
}

// 36 - defect 4: an empty data payload never completes, and a valid Anthropic
// message still does.
func TestEmptyDataAndValidAnthropic(t *testing.T) {
	c := New("anthropic", true)
	c.Feed([]byte("data:\n\ndata\n\ndata: \n\n"))
	if r := c.Finish(); r.Complete {
		t.Error("empty data must not complete")
	}

	c = New("anthropic", false)
	c.Feed([]byte(`{"type":"message","model":"c","usage":{"input_tokens":2}}`))
	if r := c.Finish(); !r.Complete || r.Failed {
		t.Errorf("valid anthropic message: %+v", r)
	}

	c = New("openai", false)
	c.Feed([]byte(`{"model":"gpt-5","choices":[{"index":0}]}`))
	if r := c.Finish(); !r.Complete || r.Failed {
		t.Errorf("openai choices object: %+v", r)
	}
}

// 37 - buffers are released at Finish and never leak the response body.
func TestFinishReleasesBuffers(t *testing.T) {
	c := New("openai", false)
	c.Feed([]byte(`{"model":"m","usage":{"prompt_tokens":1}}`))
	_ = c.Finish()
	if c.jsonBuf != nil || c.data != nil || c.raw != nil || c.line != nil {
		t.Error("Finish must release all response buffers")
	}

	s := New("anthropic", true)
	s.Feed([]byte("data: {\"type\":\"message_stop\"}\n\n"))
	_ = s.Finish()
	if s.jsonBuf != nil || s.data != nil || s.raw != nil || s.line != nil {
		t.Error("Finish must release all SSE buffers")
	}
}
