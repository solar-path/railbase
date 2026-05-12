package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGChannel is the single Postgres LISTEN/NOTIFY channel Railbase
// uses for cross-process broadcast. One channel + a `topic` field
// in the JSON payload keeps things simple — per-topic channels
// would need dynamic LISTEN/UNLISTEN matching subscribers, which
// is overkill for v0.6 traffic volumes.
const PGChannel = "railbase_events"

// MaxNotifyPayload is the Postgres NOTIFY payload limit. Docs/05
// notes:
//
//	"Limit: payload < 8000 bytes (Postgres NOTIFY limit) — для
//	 больших событий публикуется ID + ленивая дозагрузка"
//
// We keep payloads small by design (topic + opaque event id), so
// the lazy-load path doesn't kick in for v0.6. Reserve it as a
// safety net.
const MaxNotifyPayload = 7900 // 100 bytes of safety margin

// PGBridge connects an in-process Bus to other Railbase replicas
// via Postgres LISTEN/NOTIFY. Local Publish() calls fan out to:
//
//   - In-process subscribers of the local Bus (existing behaviour).
//   - All other Railbase processes LISTENing on PGChannel.
//
// Loop avoidance: the PID of the publishing process is embedded in
// every payload. The receive-side filter drops messages whose pid
// matches the local pid, so we don't re-deliver our own events back
// to ourselves.
//
// Reconnect policy: on LISTEN connection failure, log the error and
// reconnect with backoff. We do NOT replay missed events — clients
// that need durability subscribe via the realtime broker, which
// has its own resume-token model.
type PGBridge struct {
	bus  *Bus
	pool *pgxpool.Pool
	log  logHandle
	pid  int

	cancel context.CancelFunc
	done   chan struct{}

	publishedSelf publishedSelfTracker
}

// logHandle is the slim subset of *slog.Logger we need; defined as
// an interface so tests can inject a no-op.
type logHandle interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
}

// publishedSelfTracker dedupes events the local process emitted
// from its own NOTIFY echo. We tag each outbound NOTIFY with a
// monotonic counter and remember the recent set; an inbound message
// matching one of those gets silently dropped.
//
// The buffer is a fixed-size ring so memory is bounded under high
// publish rates.
type publishedSelfTracker struct {
	mu   sync.Mutex
	ring [256]uint64
	pos  int
}

func (p *publishedSelfTracker) record(seq uint64) {
	p.mu.Lock()
	p.ring[p.pos%len(p.ring)] = seq
	p.pos++
	p.mu.Unlock()
}

func (p *publishedSelfTracker) seen(seq uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range p.ring {
		if v == seq {
			return true
		}
	}
	return false
}

// NewPGBridge wires bus to pool. Call Start to begin the LISTEN
// loop. The same Bus instance can host multiple bridges (e.g. for
// future cluster + sidecar setups), but we only need one in v0.6.
func NewPGBridge(bus *Bus, pool *pgxpool.Pool, log logHandle) *PGBridge {
	if log == nil {
		log = noopLog{}
	}
	return &PGBridge{
		bus:  bus,
		pool: pool,
		log:  log,
		pid:  os.Getpid(),
	}
}

// Start spawns the LISTEN goroutine and the publish-relay
// subscription. Returns once both are running. Call Stop to wind
// it down on shutdown.
func (b *PGBridge) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	b.done = make(chan struct{})

	go b.listenLoop(ctx)

	// Local-bus → NOTIFY: subscribe to ALL topics and re-publish to
	// Postgres. Pattern "*" intentionally matches anything (it never
	// equals a real topic and the wildcard is single-level so we
	// can't accidentally cycle on our own output).
	//
	// We use a wide buffer because NOTIFY can be slow under load and
	// we'd rather drop than block the in-process publish path.
	b.bus.Subscribe("*", 1024, b.onLocalEvent)
	return nil
}

// Stop tears down the bridge. Idempotent — safe to call from a
// deferred shutdown handler even if Start failed earlier.
func (b *PGBridge) Stop() {
	if b.cancel == nil {
		return
	}
	b.cancel()
	if b.done != nil {
		<-b.done
	}
}

// onLocalEvent runs on the bus's subscriber goroutine for every
// in-process Publish. It serialises the event and emits NOTIFY so
// other replicas see it.
//
// Failures are logged but never propagated — local subscribers have
// already received the event; failing to fan out to other replicas
// shouldn't cascade back into the local handler.
func (b *PGBridge) onLocalEvent(ctx context.Context, e Event) {
	seq := nextSeq()
	b.publishedSelf.record(seq)

	payload, err := encodePayload(e, b.pid, seq)
	if err != nil {
		b.log.Warn("eventbus/pgbridge: encode failed", "topic", e.Topic, "err", err)
		return
	}
	if len(payload) > MaxNotifyPayload {
		// v0.6 payloads stay small; if a future caller wedges in a
		// fat object we reject loudly so we know to add lazy-load.
		b.log.Warn("eventbus/pgbridge: payload too large for NOTIFY (truncated would lose data)",
			"topic", e.Topic, "size", len(payload))
		return
	}
	if _, err := b.pool.Exec(ctx,
		"SELECT pg_notify($1, $2)", PGChannel, string(payload)); err != nil {
		b.log.Warn("eventbus/pgbridge: NOTIFY failed",
			"topic", e.Topic, "err", err)
	}
}

// listenLoop owns the dedicated *pgx.Conn that runs LISTEN. We
// can't use the pool's regular Acquire because LISTEN ties the
// session to the connection — releasing it back to the pool would
// drop our subscription.
//
// Reconnect on failure with exponential backoff (capped). The
// outer ctx cancels on Stop().
func (b *PGBridge) listenLoop(ctx context.Context) {
	defer close(b.done)
	backoff := 250 * time.Millisecond
	const maxBackoff = 30 * time.Second

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		err := b.runOneListen(ctx)
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		b.log.Warn("eventbus/pgbridge: listen loop ended; reconnecting",
			"err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// runOneListen owns one connection's lifecycle. Returns nil only on
// graceful shutdown; everything else is an error to retry.
func (b *PGBridge) runOneListen(ctx context.Context) error {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire: %w", err)
	}
	defer conn.Release()

	// LISTEN registers this connection's interest in the channel.
	// Subsequent WaitForNotification calls block until something
	// arrives or ctx cancels.
	if _, err := conn.Exec(ctx, fmt.Sprintf("LISTEN %s", PGChannel)); err != nil {
		return fmt.Errorf("LISTEN: %w", err)
	}
	b.log.Info("eventbus/pgbridge: listening", "channel", PGChannel, "pid", b.pid)

	for {
		notif, err := conn.Conn().WaitForNotification(ctx)
		if errors.Is(err, context.Canceled) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("wait: %w", err)
		}
		b.handleNotification(notif)
	}
}

// handleNotification decodes one inbound NOTIFY and re-publishes
// it onto the local bus. Drops messages we sent ourselves, plus
// anything that fails to parse (logged once per occurrence).
func (b *PGBridge) handleNotification(n *pgconn.Notification) {
	var p notifyPayload
	if err := json.Unmarshal([]byte(n.Payload), &p); err != nil {
		b.log.Warn("eventbus/pgbridge: malformed payload", "err", err)
		return
	}
	if p.Pid == b.pid && b.publishedSelf.seen(p.Seq) {
		// Loop avoidance: same process, already-recorded seq.
		return
	}
	b.bus.Publish(Event{Topic: p.Topic, Payload: p.Payload})
}

// notifyPayload is the on-wire shape inside Postgres NOTIFY.
type notifyPayload struct {
	Topic   string `json:"t"`
	Pid     int    `json:"p"`
	Seq     uint64 `json:"s"`
	Payload any    `json:"d"`
}

func encodePayload(e Event, pid int, seq uint64) ([]byte, error) {
	return json.Marshal(notifyPayload{
		Topic:   e.Topic,
		Pid:     pid,
		Seq:     seq,
		Payload: e.Payload,
	})
}

// nextSeq returns a process-local monotonic counter. Wraps after
// 2^64 — fine; the publishedSelfTracker ring only holds 256
// entries, so collisions on wrap are statistically irrelevant.
var seqCounter uint64
var seqMu sync.Mutex

func nextSeq() uint64 {
	seqMu.Lock()
	defer seqMu.Unlock()
	seqCounter++
	return seqCounter
}

// noopLog is the fallback when callers pass nil. Avoids nil-checks
// throughout PGBridge.
type noopLog struct{}

func (noopLog) Info(string, ...any)  {}
func (noopLog) Warn(string, ...any)  {}
func (noopLog) Error(string, ...any) {}
