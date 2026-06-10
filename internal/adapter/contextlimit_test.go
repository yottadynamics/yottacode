package adapter

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsContextOverflow(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "codex nested code+message (probed 2026-06-10)",
			err:  errors.New("openai-auth: context_length_exceeded: Your input exceeds the context window of this model. Please adjust your input and try again."),
			want: true,
		},
		{
			name: "openai chat-completions phrasing",
			err:  errors.New("This model's maximum context length is 128000 tokens. However, your messages resulted in 131072 tokens."),
			want: true,
		},
		{
			name: "anthropic phrasing",
			err:  errors.New("anthropic: prompt is too long: 210000 tokens > 200000 maximum"),
			want: true,
		},
		{
			name: "wrapped error keeps matching",
			err:  fmt.Errorf("turn failed: %w", errors.New("openai-auth: context_length_exceeded: too big")),
			want: true,
		},
		{name: "nil", err: nil, want: false},
		{name: "unrelated provider error", err: errors.New("openai-auth: HTTP 500 after 3 attempts: upstream connect error"), want: false},
		{name: "rate limit is not overflow", err: errors.New("openai-auth: the usage limit has been reached"), want: false},
		{name: "cancellation is not overflow", err: errors.New("context canceled"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsContextOverflow(tc.err); got != tc.want {
				t.Errorf("IsContextOverflow(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
