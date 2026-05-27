package adapter

import (
	"context"
	"testing"
	"time"
)

func TestSplitForAnthropic_ToolResultWithImages(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "you are helpful"},
		{Role: RoleUser, Content: "read photo.png"},
		{
			Role:    RoleAssistant,
			Content: "Reading the image.",
			ToolCalls: []ToolCall{{
				ID:       "toolu_img",
				Name:     "read_file",
				ArgsJSON: `{"path":"photo.png"}`,
			}},
		},
		{
			Role:       RoleTool,
			Content:    "[image: photo.png, image/png, 8 bytes]",
			ToolCallID: "toolu_img",
			Images: []ImageBlock{{
				Data:      []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A},
				MediaType: "image/png",
			}},
		},
	}

	system, out := splitForAnthropic(msgs)
	if len(system) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(system))
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 messages (user, assistant, tool-result), got %d", len(out))
	}

	toolResultMsg := out[2]
	if toolResultMsg.Role != "user" {
		t.Errorf("tool result role = %q, want user", toolResultMsg.Role)
	}
	if len(toolResultMsg.Content) != 1 {
		t.Fatalf("expected 1 content block (tool_result), got %d", len(toolResultMsg.Content))
	}
	toolResult := toolResultMsg.Content[0].OfToolResult
	if toolResult == nil {
		t.Fatalf("expected OfToolResult content block")
	}
	if toolResult.ToolUseID != "toolu_img" {
		t.Errorf("tool_use_id = %q, want toolu_img", toolResult.ToolUseID)
	}
	if len(toolResult.Content) != 2 {
		t.Fatalf("expected 2 result content blocks (text + image), got %d", len(toolResult.Content))
	}
	if toolResult.Content[0].OfText == nil {
		t.Error("first content block should be text")
	}
	if toolResult.Content[1].OfImage == nil {
		t.Fatal("second content block should be image")
	}
	imgBlock := toolResult.Content[1].OfImage
	if imgBlock.Source.OfBase64 == nil {
		t.Fatal("image source should be OfBase64")
	}
	if imgBlock.Source.OfBase64.MediaType != "image/png" {
		t.Errorf("media type = %q, want image/png", imgBlock.Source.OfBase64.MediaType)
	}
	if imgBlock.Source.OfBase64.Data == "" {
		t.Error("base64 data should not be empty")
	}
}

func TestSplitForAnthropic_ToolResultWithoutImages(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "read main.go"},
		{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{{
				ID: "toolu_01", Name: "read_file", ArgsJSON: `{"path":"main.go"}`,
			}},
		},
		{
			Role:       RoleTool,
			Content:    "package main\n",
			ToolCallID: "toolu_01",
		},
	}

	_, out := splitForAnthropic(msgs)
	toolResultMsg := out[2]
	toolResult := toolResultMsg.Content[0].OfToolResult
	if len(toolResult.Content) != 1 {
		t.Fatalf("expected 1 result content block (text only), got %d", len(toolResult.Content))
	}
	if toolResult.Content[0].OfText == nil {
		t.Error("content block should be text")
	}
}

func TestAnthropicAdapter_ImageInToolResult(t *testing.T) {
	captured := make(chan map[string]any, 1)
	srv := newAnthropicMockServer(t, anthropicScript{
		events: []anthropicSSE{
			{Event: "message_start", Data: `{"type":"message_start","message":{"id":"msg_img","role":"assistant","content":[],"model":"claude-sonnet-4-6"}}`},
			{Event: "content_block_start", Data: `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{Event: "content_block_delta", Data: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"I see the image."}}`},
			{Event: "content_block_stop", Data: `{"type":"content_block_stop","index":0}`},
			{Event: "message_delta", Data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`},
			{Event: "message_stop", Data: `{"type":"message_stop"}`},
		},
		captureBody: captured,
	})
	t.Cleanup(srv.Close)

	a := newAnthropicAdapter(Config{
		BaseURL: srv.URL,
		APIKey:  "k",
		Model:   "claude-sonnet-4-6",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := a.ChatStream(ctx, []Message{
		{Role: RoleSystem, Content: "you are helpful"},
		{Role: RoleUser, Content: "read photo.png"},
		{
			Role:    RoleAssistant,
			Content: "Reading the image.",
			ToolCalls: []ToolCall{{
				ID:       "toolu_img",
				Name:     "read_file",
				ArgsJSON: `{"path":"photo.png"}`,
			}},
		},
		{
			Role:       RoleTool,
			Content:    "[image: photo.png, image/png, 4 bytes]",
			ToolCallID: "toolu_img",
			Images: []ImageBlock{{
				Data:      []byte{0x89, 'P', 'N', 'G'},
				MediaType: "image/png",
			}},
		},
	}, nil)

	for range stream {
	}

	body := <-captured
	msgs, ok := body["messages"].([]any)
	if !ok {
		t.Fatalf("expected messages array")
	}
	lastMsg := msgs[len(msgs)-1].(map[string]any)
	content, ok := lastMsg["content"].([]any)
	if !ok {
		t.Fatalf("expected content array in last message")
	}
	if len(content) != 1 {
		t.Fatalf("expected 1 tool_result block, got %d", len(content))
	}
	toolResult, ok := content[0].(map[string]any)
	if !ok || toolResult["type"] != "tool_result" {
		t.Fatalf("expected tool_result block, got %v", content[0])
	}
	resultContent, ok := toolResult["content"].([]any)
	if !ok {
		t.Fatalf("expected content array in tool_result")
	}
	if len(resultContent) != 2 {
		t.Fatalf("expected 2 content blocks (text + image), got %d", len(resultContent))
	}
	textBlock := resultContent[0].(map[string]any)
	if textBlock["type"] != "text" {
		t.Errorf("first block type = %v, want text", textBlock["type"])
	}
	imageBlock := resultContent[1].(map[string]any)
	if imageBlock["type"] != "image" {
		t.Errorf("second block type = %v, want image", imageBlock["type"])
	}
	source, ok := imageBlock["source"].(map[string]any)
	if !ok {
		t.Fatalf("expected source in image block")
	}
	if source["type"] != "base64" {
		t.Errorf("source type = %v, want base64", source["type"])
	}
	if source["media_type"] != "image/png" {
		t.Errorf("media_type = %v, want image/png", source["media_type"])
	}
	data, ok := source["data"].(string)
	if !ok || data == "" {
		t.Error("expected non-empty base64 data")
	}
}
