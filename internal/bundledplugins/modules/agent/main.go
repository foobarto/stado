//go:build wasip1

// Package main is the stado `agent` bundled plugin.
// Exposes agent.spawn, agent.list, agent.read_messages,
// agent.send_message, agent.cancel via stado_agent_* Tier 1+ imports.
// EP-0038 §D.
package main

import (
	"encoding/json"

	"github.com/foobarto/stado/internal/bundledplugins/sdk"
)

func main() {}

// ── host imports ───────────────────────────────────────────────────────────

//go:wasmimport stado stado_agent_spawn
func stadoAgentSpawn(reqPtr, reqLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_agent_list
func stadoAgentList(resPtr, resCap uint32) int32

//go:wasmimport stado stado_agent_read_messages
func stadoAgentReadMessages(reqPtr, reqLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_agent_send_message
func stadoAgentSendMessage(reqPtr, reqLen uint32) int32

//go:wasmimport stado stado_agent_cancel
func stadoAgentCancel(reqPtr, reqLen, resPtr, resCap uint32) int32

// ── ABI exports ────────────────────────────────────────────────────────────

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

// stado_tool_spawn — agent.spawn
//
//go:wasmexport stado_tool_spawn
func stadoToolSpawn(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return stadoAgentSpawn(uint32(argsPtr), uint32(argsLen), uint32(resPtr), uint32(resCap))
}

// stado_tool_list — agent.list
//
//go:wasmexport stado_tool_list
func stadoToolList(argsPtr, argsLen, resPtr, resCap int32) int32 {
	// args ignored; list returns all agents in caller's tree
	return stadoAgentList(uint32(resPtr), uint32(resCap))
}

// stado_tool_read_messages — agent.read_messages
//
//go:wasmexport stado_tool_read_messages
func stadoToolReadMessages(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return stadoAgentReadMessages(uint32(argsPtr), uint32(argsLen), uint32(resPtr), uint32(resCap))
}

// stado_tool_send_message — agent.send_message
//
//go:wasmexport stado_tool_send_message
func stadoToolSendMessage(argsPtr, argsLen, resPtr, resCap int32) int32 {
	n := stadoAgentSendMessage(uint32(argsPtr), uint32(argsLen))
	if n < 0 {
		return writeError(resPtr, resCap, "send_message failed")
	}
	b, _ := json.Marshal(map[string]bool{"ok": true})
	return writeResult(resPtr, resCap, b)
}

// stado_tool_cancel — agent.cancel
//
//go:wasmexport stado_tool_cancel
func stadoToolCancel(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return stadoAgentCancel(uint32(argsPtr), uint32(argsLen), uint32(resPtr), uint32(resCap))
}

func writeError(resPtr, resCap int32, msg string) int32 {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return writeResult(resPtr, resCap, b)
}

func writeResult(resPtr, resCap int32, data []byte) int32 {
	if int32(len(data)) > resCap {
		return -1
	}
	return sdk.Write(resPtr, data)
}
