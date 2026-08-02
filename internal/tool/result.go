package tool

// CallStatus is the normalized lifecycle result of a tool call.
type CallStatus string

const (
	CallStatusOK      CallStatus = "ok"
	CallStatusError   CallStatus = "error"
	CallStatusBlocked CallStatus = "blocked"
	CallStatusWaiting CallStatus = "waiting"
)

// Normalize fills the structured result fields while preserving the legacy
// Content and IsError fields used by existing handlers and clients.
func (r *CallResult) Normalize() {
	if r == nil {
		return
	}
	if r.Summary == "" {
		r.Summary = r.Content
	}
	if r.Status == "" {
		switch {
		case r.RequiresUser:
			r.Status = CallStatusWaiting
		case r.IsError:
			r.Status = CallStatusError
		default:
			r.Status = CallStatusOK
		}
	}
	if r.Status == CallStatusBlocked {
		r.IsError = true
	}
}
