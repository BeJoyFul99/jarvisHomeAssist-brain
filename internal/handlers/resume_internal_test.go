package handlers

import "testing"

func TestExtractResumeJSON(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"plain object", `{"summary":"a","keywords":["go"]}`, false},
		{"fenced json", "```json\n{\"summary\":\"a\"}\n```", false},
		{"fenced no lang", "```\n{\"summary\":\"a\"}\n```", false},
		{"prose wrapped", "Here is your resume:\n{\"summary\":\"a\"}\nGood luck!", false},
		{"no json", "sorry, I cannot help with that", true},
		{"broken json", `{"summary": "a`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := extractResumeJSON(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %s", string(out))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(out) == 0 {
				t.Fatal("expected non-empty JSON")
			}
		})
	}
}
