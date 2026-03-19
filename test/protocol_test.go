package protocol_test

import (
	"encoding/json"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"bingo-bis/internal/protocol"
)

// TestProtocol is the single Go testing entry point Ginkgo needs.
func TestProtocol(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Protocol Suite")
}

// ─── fixtures ────────────────────────────────────────────────────────────────

var sampleBreakpoint = protocol.Breakpoint{
	ID:      1,
	Enabled: true,
	Location: protocol.Location{
		File:     "main.go",
		Line:     42,
		Function: "main.main",
	},
}

var sampleGoroutine = protocol.Goroutine{
	ID:     1,
	Status: "waiting",
	CurrentLoc: protocol.Location{
		File: "main.go",
		Line: 42,
	},
}

var sampleFrames = []protocol.Frame{
	{Index: 0, Location: protocol.Location{File: "main.go", Line: 42, Function: "main.main"}},
	{Index: 1, Location: protocol.Location{File: "runtime/proc.go", Line: 271, Function: "runtime.main"}},
}

// ─── specs ───────────────────────────────────────────────────────────────────

var _ = Describe("Event", func() {

	Describe("NewEvent", func() {
		Context("with a valid payload", func() {
			var (
				payload protocol.BreakpointHitPayload
				event   protocol.Event
			)

			BeforeEach(func() {
				payload = protocol.BreakpointHitPayload{
					Breakpoint: sampleBreakpoint,
					Goroutine:  sampleGoroutine,
					Frames:     sampleFrames,
				}
				var err error
				event, err = protocol.NewEvent(protocol.EventBreakpointHit, 7, payload)
				Expect(err).NotTo(HaveOccurred())
			})

			It("stamps the current protocol version", func() {
				Expect(event.Version).To(Equal(protocol.Version))
			})

			It("preserves the kind", func() {
				Expect(event.Kind).To(Equal(protocol.EventBreakpointHit))
			})

			It("preserves the sequence number", func() {
				Expect(event.Seq).To(Equal(uint64(7)))
			})

			It("serialises the payload to non-empty JSON", func() {
				Expect(event.Payload).NotTo(BeEmpty())
			})
		})

		Context("with an un-marshallable payload", func() {
			It("returns an error", func() {
				_, err := protocol.NewEvent(protocol.EventError, 0, make(chan int))
				Expect(err).To(HaveOccurred())
			})
		})
	})

	Describe("MustEvent", func() {
		It("panics when the payload cannot be marshalled", func() {
			Expect(func() {
				protocol.MustEvent(protocol.EventError, 0, make(chan int))
			}).To(Panic())
		})

		It("does not panic for valid payloads", func() {
			Expect(func() {
				protocol.MustEvent(protocol.EventContinued, 1, protocol.ContinuedPayload{})
			}).NotTo(Panic())
		})
	})

	Describe("wire round-trip", func() {
		It("survives marshal → unmarshal with payload intact", func() {
			original := protocol.BreakpointHitPayload{
				Breakpoint: sampleBreakpoint,
				Goroutine:  sampleGoroutine,
				Frames:     sampleFrames,
			}
			event, err := protocol.NewEvent(protocol.EventBreakpointHit, 3, original)
			Expect(err).NotTo(HaveOccurred())

			wire, err := protocol.MarshalEvent(event)
			Expect(err).NotTo(HaveOccurred())
			Expect(wire).NotTo(BeEmpty())

			decoded, err := protocol.UnmarshalEvent(wire)
			Expect(err).NotTo(HaveOccurred())
			Expect(decoded.Kind).To(Equal(protocol.EventBreakpointHit))
			Expect(decoded.Seq).To(Equal(uint64(3)))

			var got protocol.BreakpointHitPayload
			Expect(protocol.DecodeEventPayload(decoded, &got)).To(Succeed())
			Expect(got.Breakpoint.ID).To(Equal(original.Breakpoint.ID))
			Expect(got.Breakpoint.Location.File).To(Equal(original.Breakpoint.Location.File))
			Expect(got.Goroutine.Status).To(Equal(original.Goroutine.Status))
			Expect(got.Frames).To(HaveLen(len(original.Frames)))
		})
	})

	Describe("UnmarshalEvent", func() {
		It("returns an error for malformed JSON", func() {
			_, err := protocol.UnmarshalEvent([]byte("not json {{"))
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("DecodeEventPayload", func() {
		It("does not panic when decoding into a mismatched struct", func() {
			event, err := protocol.NewEvent(protocol.EventError, 1, protocol.ErrorPayload{
				Message: "something went wrong",
			})
			Expect(err).NotTo(HaveOccurred())

			// json.Unmarshal is lenient — mismatched fields are silently ignored.
			// The important guarantee is no panic.
			var wrong protocol.BreakpointHitPayload
			Expect(func() {
				_ = protocol.DecodeEventPayload(event, &wrong)
			}).NotTo(Panic())
		})
	})
})

var _ = Describe("Command", func() {

	Describe("wire round-trip", func() {
		It("survives marshal → unmarshal with payload intact", func() {
			original := protocol.SetBreakpointPayload{
				File: "server.go",
				Line: 100,
			}
			raw, err := json.Marshal(original)
			Expect(err).NotTo(HaveOccurred())

			cmd := protocol.Command{
				Version: protocol.Version,
				Kind:    protocol.CmdSetBreakpoint,
				Payload: raw,
			}

			wire, err := json.Marshal(cmd)
			Expect(err).NotTo(HaveOccurred())

			decoded, err := protocol.UnmarshalCommand(wire)
			Expect(err).NotTo(HaveOccurred())
			Expect(decoded.Kind).To(Equal(protocol.CmdSetBreakpoint))
			Expect(decoded.Version).To(Equal(protocol.Version))

			var got protocol.SetBreakpointPayload
			Expect(protocol.DecodeCommandPayload(decoded, &got)).To(Succeed())
			Expect(got.File).To(Equal(original.File))
			Expect(got.Line).To(Equal(original.Line))
		})
	})

	Describe("UnmarshalCommand", func() {
		It("returns an error for malformed JSON", func() {
			_, err := protocol.UnmarshalCommand([]byte("not json"))
			Expect(err).To(HaveOccurred())
		})

		It("returns an error for an empty byte slice", func() {
			_, err := protocol.UnmarshalCommand([]byte{})
			Expect(err).To(HaveOccurred())
		})
	})
})

var _ = Describe("Kind constants", func() {

	It("defines no empty EventKind values", func() {
		kinds := []protocol.EventKind{
			protocol.EventBreakpointHit,
			protocol.EventPanic,
			protocol.EventOutput,
			protocol.EventProcessExited,
			protocol.EventBreakpointSet,
			protocol.EventBreakpointCleared,
			protocol.EventStepped,
			protocol.EventContinued,
			protocol.EventLocals,
			protocol.EventFrames,
			protocol.EventGoroutines,
			protocol.EventError,
		}
		for _, k := range kinds {
			Expect(string(k)).NotTo(BeEmpty(), "EventKind constant should not be empty")
		}
	})

	It("defines no empty CommandKind values", func() {
		kinds := []protocol.CommandKind{
			protocol.CmdLaunch,
			protocol.CmdAttach,
			protocol.CmdKill,
			protocol.CmdSetBreakpoint,
			protocol.CmdClearBreakpoint,
			protocol.CmdContinue,
			protocol.CmdStepOver,
			protocol.CmdStepInto,
			protocol.CmdStepOut,
			protocol.CmdLocals,
			protocol.CmdFrames,
			protocol.CmdGoroutines,
		}
		for _, k := range kinds {
			Expect(string(k)).NotTo(BeEmpty(), "CommandKind constant should not be empty")
		}
	})
})
