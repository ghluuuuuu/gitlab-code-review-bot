package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/config/template"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/llm"
	"github.com/ghluuuuuu/gitlab-code-review-bot/internal/ocr/model"
)

type changeAnalysisLLM struct {
	request llm.ChatRequest
}

func (f *changeAnalysisLLM) CompletionsWithCtx(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	f.request = request
	content := "### 涉及的功能模块\n- 设备命令模块\n\n### 运维配置更新\n- 新增 `COMMAND_TIMEOUT`\n\n### 建议测试范围\n- 命令超时集成测试"
	return &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.ResponseMessage{Content: &content}}}}, nil
}

func TestGenerateChangeAnalysisIncludesModulesOperationsAndTests(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	client := &changeAnalysisLLM{}
	tpl := template.Template{
		MaxTokens: 58888,
		ChangeAnalysisTask: &template.LlmConversation{Messages: []template.ChatMessage{
			{Role: "system", Content: "analyze change"},
			{Role: "user", Content: "{{requirement_background}}\n{{changed_files}}\n{{diffs}}"},
		}},
	}
	agent := New(Args{RepoDir: t.TempDir(), Template: tpl, LLMClient: client, Model: "model", Background: "device commands"})
	agent.diffs = []model.Diff{{OldPath: "config.go", NewPath: "config.go", Diff: "@@ -1 +1 @@\n-timeout=10\n+timeout=20"}}
	if err := agent.generateChangeAnalysis(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, heading := range []string{"涉及的功能模块", "运维配置更新", "建议测试范围"} {
		if !strings.Contains(agent.ChangeAnalysis(), heading) {
			t.Fatalf("change analysis missing %q: %s", heading, agent.ChangeAnalysis())
		}
	}
	if client.request.MaxTokens != 3000 || len(client.request.Messages) != 2 {
		t.Fatalf("unexpected analysis request: %#v", client.request)
	}
	userText := client.request.Messages[1].ExtractText()
	if !strings.Contains(userText, "device commands") || !strings.Contains(userText, "config.go") || !strings.Contains(userText, "+timeout=20") {
		t.Fatalf("analysis request omitted change context: %s", userText)
	}
}
