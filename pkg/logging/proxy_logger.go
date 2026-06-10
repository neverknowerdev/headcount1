package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type ProxyLogger struct {
	mu       sync.Mutex
	file     *os.File
	basePath string
}

func NewProxyLogger(basePath, companyShortName string, taskID int32, runID int32) (*ProxyLogger, error) {
	logDir := filepath.Join(basePath, "data", companyShortName, "logs", fmt.Sprintf("%d", taskID))
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	logFile := filepath.Join(logDir, fmt.Sprintf("run-%d.log", runID))
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	return &ProxyLogger{
		file:     f,
		basePath: basePath,
	}, nil
}

func (l *ProxyLogger) LogRequest(model, agentName, providerName string, requestBody []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	l.file.WriteString(fmt.Sprintf("\n=== LLM Request [%s] ===\n", ts))
	l.file.WriteString(fmt.Sprintf("Model: %s\n", model))
	l.file.WriteString(fmt.Sprintf("Agent: %s\n", agentName))
	l.file.WriteString(fmt.Sprintf("Provider: %s\n", providerName))
	l.file.WriteString("---\n")
	l.file.Write(requestBody)
	l.file.WriteString("\n")
}

func (l *ProxyLogger) LogResponse(model, providerName string, statusCode int, responseBody []byte, promptTokens, completionTokens, totalTokens int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	l.file.WriteString(fmt.Sprintf("\n=== LLM Response [%s] ===\n", ts))
	l.file.WriteString(fmt.Sprintf("Model: %s\n", model))
	l.file.WriteString(fmt.Sprintf("Provider: %s\n", providerName))
	l.file.WriteString(fmt.Sprintf("Status: %d\n", statusCode))
	l.file.WriteString(fmt.Sprintf("Tokens: prompt=%d completion=%d total=%d\n", promptTokens, completionTokens, totalTokens))
	l.file.WriteString("---\n")
	l.file.Write(responseBody)
	l.file.WriteString("\n")
}

func (l *ProxyLogger) LogStreamResponse(model, providerName string, content, reasoningContent string, promptTokens, completionTokens, totalTokens int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	l.file.WriteString(fmt.Sprintf("\n=== LLM Response [%s] ===\n", ts))
	l.file.WriteString(fmt.Sprintf("Model: %s\n", model))
	l.file.WriteString(fmt.Sprintf("Provider: %s\n", providerName))
	l.file.WriteString(fmt.Sprintf("Tokens: prompt=%d completion=%d total=%d\n", promptTokens, completionTokens, totalTokens))
	l.file.WriteString("---\n")
	if reasoningContent != "" {
		l.file.WriteString(fmt.Sprintf("[Reasoning]\n%s\n", reasoningContent))
	}
	if content != "" {
		l.file.WriteString(fmt.Sprintf("[Content]\n%s\n", content))
	}
	if reasoningContent == "" && content == "" {
		l.file.WriteString("(no content)\n")
	}
}

func (l *ProxyLogger) LogError(model, agentName, providerName string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	ts := time.Now().UTC().Format(time.RFC3339)
	l.file.WriteString(fmt.Sprintf("\n=== LLM Error [%s] ===\n", ts))
	l.file.WriteString(fmt.Sprintf("Model: %s\n", model))
	l.file.WriteString(fmt.Sprintf("Agent: %s\n", agentName))
	l.file.WriteString(fmt.Sprintf("Provider: %s\n", providerName))
	l.file.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))
	l.file.WriteString("\n")
}

func (l *ProxyLogger) Close() error {
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}
