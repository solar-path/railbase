package mailer

// Bus topics for mailer events. Published only when the Mailer is
// constructed with an Options.Bus — nil bus keeps the package zero-cost
// (current default) for callers that don't need hooks.
//
// Layout note: before_send is published SYNCHRONOUSLY via PublishSync
// so a hook can mutate the message or veto it; after_send is async via
// Publish (observers don't block the response).
const (
	// TopicBeforeSend is the synchronous, pre-driver hook point.
	// Payload: *MailerBeforeSendEvent. Subscribers may mutate
	// event.Message (pointer) and set event.Reject = true to abort
	// the send with a ReasonString surfaced in the returned error.
	TopicBeforeSend = "mailer.before_send"

	// TopicAfterSend is the asynchronous post-driver observation point.
	// Payload: MailerAfterSendEvent (by value — read-only snapshot).
	// Fires for both success and failure (Err carries the outcome).
	TopicAfterSend = "mailer.after_send"
)

// MailerBeforeSendEvent is published synchronously before each mailer
// send. Subscribers may MUTATE the message (e.g. replace From, add an
// X-Tenant header, rewrite a templated link) and may CANCEL the send
// by setting Reject = true.
//
// Subscribers run serially in registration order on the caller's
// goroutine. A panicking subscriber halts the chain — keep handlers
// defensive.
//
// Why a pointer field: the contract is "this hook can change the
// outbound message". A value field would silently lose mutations.
type MailerBeforeSendEvent struct {
	// Message is the outbound message. Mutate fields in place; the
	// driver will see the mutated state.
	Message *Message
	// Reject, when true, aborts the send. The driver is NOT called.
	// SendDirect / SendTemplate return an error containing Reason.
	Reject bool
	// Reason is the operator-facing string surfaced in the error
	// when Reject is true. Optional; empty defaults to "rejected".
	Reason string
}

// MailerAfterSendEvent is published asynchronously after the driver
// returns (success or failure). Subscribers are for OBSERVATION ONLY
// — Message is a value-copy snapshot and changes are not visible to
// anyone else.
//
// Err is the driver's return value (nil on success, non-nil on
// failure — including transient errors and rejected-by-driver errors).
type MailerAfterSendEvent struct {
	Message Message
	Err     error
}
