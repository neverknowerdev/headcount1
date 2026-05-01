package utils

import "testing"

func TestBuildProviderURL(t *testing.T) {
	tests := []struct {
		name     string
		baseUrl  string
		endpoint string
		expected string
	}{
		{
			name:     "base URL with /v1 suffix",
			baseUrl:  "https://api.openai.com/v1",
			endpoint: "/chat/completions",
			expected: "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "base URL without /v1",
			baseUrl:  "https://api.openai.com",
			endpoint: "/chat/completions",
			expected: "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "base URL already has full path",
			baseUrl:  "https://opencode.ai/zen/go/v1/chat/completions",
			endpoint: "/chat/completions",
			expected: "https://opencode.ai/zen/go/v1/chat/completions",
		},
		{
			name:     "base URL with trailing slash",
			baseUrl:  "https://api.openai.com/v1/",
			endpoint: "/chat/completions",
			expected: "https://api.openai.com/v1/chat/completions",
		},
		{
			name:     "anthropic messages endpoint",
			baseUrl:  "https://api.anthropic.com/v1",
			endpoint: "/messages",
			expected: "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "base URL with /v1 already has messages",
			baseUrl:  "https://api.anthropic.com/v1/messages",
			endpoint: "/messages",
			expected: "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "opencode go provider",
			baseUrl:  "https://opencode.ai/zen/go/v1",
			endpoint: "/chat/completions",
			expected: "https://opencode.ai/zen/go/v1/chat/completions",
		},
		{
			name:     "dashscope provider",
			baseUrl:  "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
			endpoint: "/chat/completions",
			expected: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildProviderURL(tt.baseUrl, tt.endpoint)
			if result != tt.expected {
				t.Errorf("BuildProviderURL(%q, %q) = %q, want %q", tt.baseUrl, tt.endpoint, result, tt.expected)
			}
		})
	}
}
