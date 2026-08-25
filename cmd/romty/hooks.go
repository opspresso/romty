package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/opspresso/romty/internal/client"
	"github.com/opspresso/romty/internal/model"
	"github.com/opspresso/romty/internal/paths"
	"github.com/opspresso/romty/internal/protocol"
)

const maxHookInputBytes = 1 << 20

type hookInput struct {
	SessionID        string `json:"session_id"`
	HookEvent        string `json:"hook_event_name"`
	ToolName         string `json:"tool_name"`
	NotificationType string `json:"notification_type"`
	PermissionMode   string `json:"permission_mode"`
	BackgroundTasks  []struct {
		Status string `json:"status"`
	} `json:"background_tasks"`
	SessionCrons []struct{} `json:"session_crons"`
}

func runHookCommand(provider string, input io.Reader) {
	tabID := os.Getenv("ROMTY_TAB_ID")
	if tabID == "" {
		return
	}
	event, err := decodeHookEvent(provider, input)
	if err != nil {
		return
	}
	runtime, err := paths.Resolve()
	if err != nil {
		return
	}
	_ = client.New(runtime.Socket).ReportAgentEvent(tabID, event)
}

func decodeHookEvent(provider string, input io.Reader) (protocol.AgentEvent, error) {
	agent := model.Agent(provider)
	if agent != model.AgentClaude && agent != model.AgentCodex {
		return protocol.AgentEvent{}, errors.New("unsupported agent")
	}
	limited := &io.LimitedReader{R: input, N: maxHookInputBytes + 1}
	decoder := json.NewDecoder(limited)
	var value hookInput
	if err := decoder.Decode(&value); err != nil {
		return protocol.AgentEvent{}, err
	}
	var trailing struct{}
	trailingErr := decoder.Decode(&trailing)
	if limited.N == 0 {
		return protocol.AgentEvent{}, errors.New("hook payload is too large")
	}
	if !errors.Is(trailingErr, io.EOF) {
		if trailingErr == nil {
			return protocol.AgentEvent{}, errors.New("multiple hook payloads")
		}
		return protocol.AgentEvent{}, trailingErr
	}
	for _, metadata := range []string{
		value.SessionID, value.HookEvent,
		value.ToolName, value.NotificationType, value.PermissionMode,
	} {
		if len(metadata) > 512 {
			return protocol.AgentEvent{}, errors.New("hook metadata is too long")
		}
	}
	if value.HookEvent == "" {
		return protocol.AgentEvent{}, errors.New("hook event is required")
	}
	background := len(value.SessionCrons) > 0
	for _, task := range value.BackgroundTasks {
		switch strings.ToLower(task.Status) {
		case "running", "pending", "in_progress":
			background = true
		}
	}
	return protocol.AgentEvent{
		Agent:            agent,
		SessionID:        value.SessionID,
		HookEvent:        value.HookEvent,
		ToolName:         value.ToolName,
		NotificationType: value.NotificationType,
		PermissionMode:   value.PermissionMode,
		Background:       background,
	}, nil
}
