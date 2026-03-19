// Package protocol defines all message types exchanged between the hub and
// connected clients over WebSocket. Both directions use JSON envelopes with a
// typed Kind field; all payload structs live in this package so that the hub,
// the debugger, and the client SDK share exactly one definition of truth.
package protocol

import "encoding/json"

// Version is included in every envelope. Clients should reject messages whose
// version major component differs from their own.
const Version = "1.0"

// ─── Directions ──────────────────────────────────────────────────────────────
//
//   Event   server → all clients   something happened in the debugged process
//   Command client → server        a client wants the debugger to do something

// Event is the envelope for all server-to-client messages.
type Event struct {
	Version string          `json:"v"`
	Kind    EventKind       `json:"kind"`
	Seq     uint64          `json:"seq"` // used to detect missed packets, incremented each time a new event is sent
	Payload json.RawMessage `json:"payload"`
}

// Command is the envelope for all client-to-server messages.
type Command struct {
	Version string          `json:"v"`
	Kind    CommandKind     `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// ─── Event kinds ─────────────────────────────────────────────────────────────

type EventKind string

const (
	// Execution events — these cause the hub to suspend the debugger and wait
	// for a client command before allowing the process to continue.
	EventBreakpointHit EventKind = "BreakpointHit"
	EventPanic         EventKind = "Panic"

	// Informational events — broadcast to all clients; hub does not suspend.
	EventOutput        EventKind = "Output"
	EventProcessExited EventKind = "ProcessExited"

	// Debugger state confirmations — sent after a command is executed.
	EventBreakpointSet     EventKind = "BreakpointSet"
	EventBreakpointCleared EventKind = "BreakpointCleared"
	EventStepped           EventKind = "Stepped"
	EventContinued         EventKind = "Continued"

	// Inspection results — responses to locals/frames/goroutines queries.
	EventLocals     EventKind = "Locals"
	EventFrames     EventKind = "Frames"
	EventGoroutines EventKind = "Goroutines"

	// Error — sent when a command fails; never suspends.
	EventError EventKind = "Error"
)

// ─── Command kinds ────────────────────────────────────────────────────────────

type CommandKind string

const (
	// Process lifecycle.
	CmdLaunch CommandKind = "Launch"
	CmdAttach CommandKind = "Attach"
	CmdKill   CommandKind = "Kill"

	// Breakpoints.
	CmdSetBreakpoint   CommandKind = "SetBreakpoint"
	CmdClearBreakpoint CommandKind = "ClearBreakpoint"

	// Execution control — only valid while the process is suspended.
	CmdContinue CommandKind = "Continue"
	CmdStepOver CommandKind = "StepOver"
	CmdStepInto CommandKind = "StepInto"
	CmdStepOut  CommandKind = "StepOut"

	// Inspection — only valid while the process is suspended.
	CmdLocals     CommandKind = "Locals"
	CmdFrames     CommandKind = "Frames"
	CmdGoroutines CommandKind = "Goroutines"
)
