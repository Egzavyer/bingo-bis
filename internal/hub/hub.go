// Package hub coordinates a single debug session: it bridges connected
// WebSocket clients with a Debugger instance.
//
// Lifecycle:
//
//	h := hub.New(dbg, log)
//	go h.Run(ctx)        // blocks until ctx cancelled or last client leaves
//	h.AddClient(conn)    // called from the HTTP handler on each WS upgrade
package hub

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"bingo-bis/internal/debugger"
	"bingo-bis/internal/protocol"
)

// suspendingEvents are event kinds that cause the hub to pause and wait for
// a resuming command before the process is allowed to continue running.
var suspendingEvents = map[protocol.EventKind]bool{
	protocol.EventBreakpointHit: true,
	protocol.EventPanic:         true,
}

// resumingCommands are commands that unblock a suspended hub.
var resumingCommands = map[protocol.CommandKind]bool{
	protocol.CmdContinue: true,
	protocol.CmdStepOver: true,
	protocol.CmdStepInto: true,
	protocol.CmdStepOut:  true,
	protocol.CmdKill:     true,
}

// Hub owns one debug session. It bridges the Debugger with all connected
// WebSocket clients, fanning events out and serialising commands in.
type Hub struct {
	dbg      debugger.Debugger
	registry *registry
	log      *slog.Logger

	// cmdCh carries non-resuming commands from client read-pumps to the
	// hub's main loop. Buffered so read-pumps don't block.
	cmdCh chan clientCommand

	// resumeCh carries the first resuming command while the hub is suspended.
	// Capacity 1: first-write-wins; extras are dropped in injectCommand.
	resumeCh chan protocol.Command

	// seq is the single sequence counter for ALL events broadcast by the hub.
	// Both debugger events and hub-synthesised events (confirmations, errors)
	// are stamped with this counter before being sent to clients, so every
	// client sees one monotonically increasing stream and can detect gaps.
	seq atomic.Uint64

	// shutdownOnce ensures Kill and registry teardown happen exactly once,
	// even when ctx.Done() and last-client-disconnect race.
	shutdownOnce sync.Once

	// done is closed when Run returns.
	done chan struct{}
}

type clientCommand struct {
	cmd    protocol.Command
	client *Client
}

// New creates a Hub wired to dbg.
func New(dbg debugger.Debugger, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{
		dbg:      dbg,
		registry: newRegistry(),
		cmdCh:    make(chan clientCommand, 32),
		resumeCh: make(chan protocol.Command, 1),
		done:     make(chan struct{}),
		log:      log,
	}
}

// Run starts the hub's event loop. It blocks until:
//   - ctx is cancelled, or
//   - the debugger shuts down (dbg.Events() closes), or
//   - the last client disconnects.
//
// Safe to call exactly once.
func (h *Hub) Run(ctx context.Context) {
	defer func() {
		h.shutdown()
		close(h.done)
	}()

	for {
		select {
		case <-ctx.Done():
			return

		case evt, ok := <-h.dbg.Events():
			if !ok {
				return // debugger shut down
			}
			h.handleEvent(ctx, evt)

		case cc := <-h.cmdCh:
			h.executeCommand(cc.cmd)
		}
	}
}

// Done returns a channel closed when Run returns.
func (h *Hub) Done() <-chan struct{} {
	return h.done
}

// AddClient registers conn as a new WebSocket client and starts its pumps.
// Safe to call from any goroutine (typically the HTTP upgrade handler).
func (h *Hub) AddClient(conn WSConn, log *slog.Logger) *Client {
	c := newClient(conn, h, log)
	h.registry.add(c)
	go c.writePump()
	go c.readPump()
	h.log.Info("client connected", "total", h.registry.count())
	return c
}

// removeClient is called by a client's readPump when the connection closes.
func (h *Hub) removeClient(c *Client) {
	h.registry.remove(c)
	remaining := h.registry.count()
	h.log.Info("client disconnected", "remaining", remaining)
	if remaining == 0 {
		h.log.Info("last client disconnected — shutting down")
		// Run in a separate goroutine: readPump must not block on dbg.Kill().
		go h.shutdown()
	}
}

// ── Event handling ────────────────────────────────────────────────────────────

// handleEvent re-stamps evt with the hub's seq, broadcasts it to all clients,
// and — for suspending events — blocks until a resuming command arrives or the
// session ends.
//
// Re-stamping is necessary because the debugger engine has its own seq counter,
// and the hub synthesises additional events (errors, confirmations). Without
// re-stamping, clients would see two overlapping monotonic sequences.
func (h *Hub) handleEvent(ctx context.Context, evt protocol.Event) {
	evt.Seq = h.seq.Add(1)
	h.broadcast(evt)

	if !suspendingEvents[evt.Kind] {
		return
	}

	h.log.Info("suspended — waiting for resuming command", "event", evt.Kind)

	timeout := time.NewTimer(30 * time.Minute)
	defer timeout.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case nextEvt, ok := <-h.dbg.Events():
			// A debugger event arrived while we are suspended. The most
			// important case is ProcessExited: if the process exits while
			// paused (e.g. Kill was called externally), we must broadcast it
			// and stop waiting — there is nobody left to send a resume command.
			// For any other event we broadcast it and keep waiting; such events
			// should not normally arrive while the process is stopped, but we
			// handle them defensively.
			if !ok {
				return // debugger shut down
			}
			nextEvt.Seq = h.seq.Add(1)
			h.broadcast(nextEvt)
			if nextEvt.Kind == protocol.EventProcessExited {
				return
			}

		case cmd := <-h.resumeCh:
			h.log.Info("resuming", "command", cmd.Kind)
			h.executeCommand(cmd)
			return

		case cc := <-h.cmdCh:
			// Non-resuming command while suspended (SetBreakpoint, Locals, …).
			// Execute immediately — process is paused — then keep waiting.
			h.executeCommand(cc.cmd)

		case <-timeout.C:
			h.log.Warn("30-minute suspend timeout — auto-continuing")
			if err := h.dbg.Continue(); err != nil {
				h.log.Warn("auto-continue failed", "err", err)
			}
			return
		}
	}
}

// ── Command execution ─────────────────────────────────────────────────────────

// executeCommand dispatches cmd to the debugger and broadcasts any synchronous
// confirmation event. Errors are broadcast as EventError to all clients.
func (h *Hub) executeCommand(cmd protocol.Command) {
	result, err := dispatch(h.dbg, cmd)
	if err != nil {
		h.log.Warn("command failed", "kind", cmd.Kind, "err", err)
		h.broadcastError(cmd.Kind, err)
		return
	}
	if result.event != nil {
		result.event.Seq = h.seq.Add(1)
		h.broadcast(*result.event)
	}
}

// injectCommand is called by client read-pumps to deliver a parsed command.
// Resuming commands go to resumeCh to directly unblock a suspended hub;
// all other commands go to cmdCh for the main loop to process.
func (h *Hub) injectCommand(_ *Client, cmd protocol.Command) {
	if resumingCommands[cmd.Kind] {
		select {
		case h.resumeCh <- cmd:
		default:
			// A resuming command is already queued — first writer wins.
		}
		return
	}
	select {
	case h.cmdCh <- clientCommand{cmd: cmd}:
	default:
		h.log.Warn("command queue full — dropping", "kind", cmd.Kind)
	}
}

// ── Broadcast ─────────────────────────────────────────────────────────────────

func (h *Hub) broadcast(evt protocol.Event) {
	wire, err := protocol.MarshalEvent(evt)
	if err != nil {
		h.log.Error("marshal event failed", "err", err)
		return
	}
	h.registry.broadcast(wire)
}

func (h *Hub) broadcastError(kind protocol.CommandKind, err error) {
	evt, e := protocol.NewEvent(protocol.EventError, h.seq.Add(1), protocol.ErrorPayload{
		Command: kind,
		Message: err.Error(),
	})
	if e != nil {
		return
	}
	h.broadcast(evt)
}

// ── Shutdown ──────────────────────────────────────────────────────────────────

// shutdown closes all client connections and kills the debugger exactly once.
// Safe to call concurrently from the ctx.Done path and last-client-disconnect.
func (h *Hub) shutdown() {
	h.shutdownOnce.Do(func() {
		h.log.Info("hub shutting down")
		h.registry.closeAll()
		_ = h.dbg.Kill()
	})
}
