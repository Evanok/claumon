package ingest

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSlimLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string // "" means dropped; "same" means unchanged
	}{
		{"plain assistant line", `{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":12}}}`, "same"},
		{"api-request dropped", `{"type":"api-request","request":{"body":"..."}}`, ""},
		{"api-request-blob dropped", `{"type":"api-request-blob","data":"xyz"}`, ""},
		{"api-request-shape dropped", `{"type":"api-request-shape","shape":{}}`, ""},
		{"text mentioning api-request kept", `{"type":"user","message":{"role":"user","content":"what is \"api-request\"?"}}`, "same"},
		{
			"toolUseResult removed",
			`{"type":"user","uuid":"u1","toolUseResult":{"stdout":"big"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"big"}]}}`,
			`{"message":{"content":[{"content":"big","tool_use_id":"t1","type":"tool_result"}],"role":"user"},"type":"user","uuid":"u1"}`,
		},
		{
			"image data emptied, nested too",
			`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"look <b>&"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo"}},{"type":"tool_result","tool_use_id":"t2","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}}`,
			`{"message":{"content":[{"text":"look <b>&","type":"text"},{"source":{"data":"","media_type":"image/png","type":"base64"},"type":"image"},{"content":[{"source":{"data":"","media_type":"image/png","type":"base64"},"type":"image"}],"tool_use_id":"t2","type":"tool_result"}],"role":"user"},"type":"user"}`,
		},
		{"big numbers kept exactly", `{"type":"assistant","toolUseResult":1,"n":12345678901234567890,"f":0.1}`, `{"f":0.1,"n":12345678901234567890,"type":"assistant"}`},
		{"invalid JSON kept", `{"type":"api-request", broken`, "same"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SlimLine([]byte(tt.in + "\n"))
			switch tt.want {
			case "":
				if got != nil {
					t.Errorf("got %q, want the line dropped", got)
				}
				return
			case "same":
				if string(got) != tt.in+"\n" {
					t.Errorf("got %q, want the line unchanged", got)
				}
				return
			}
			if string(got) != tt.want+"\n" {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
			if !json.Valid(got) || !strings.HasSuffix(string(got), "\n") || strings.Count(string(got), "\n") != 1 {
				t.Errorf("result is not one JSON line: %q", got)
			}
		})
	}
}
