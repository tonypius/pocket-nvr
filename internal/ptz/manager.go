package ptz

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"pocketnvr/internal/config"
)

// Errors returned by Manager.Move and Manager.Stop.
var (
	ErrNotPTZ      = errors.New("camera has no PTZ")
	ErrUnsupported = errors.New("camera does not support that movement")
)

const (
	defaultProbeTimeout   = 3 * time.Second
	defaultCommandTimeout = 3 * time.Second
	defaultMaxMove        = 3 * time.Second
	defaultCapsTTL        = 10 * time.Minute
	defaultFailureTTL     = time.Minute
	// defaultAuthFailureTTL is how long a rejected login is remembered. It is
	// long on purpose: some firmware locks the account or source IP after a few
	// failed logins, which could take recording down with it.
	defaultAuthFailureTTL = 30 * time.Minute
	defaultStopRetryDelay = 500 * time.Millisecond

	// maxStopAttempts bounds how often the safety timer tries to deliver a
	// Stop that keeps failing (the camera's own move timeout is the last
	// line of defence after that).
	maxStopAttempts = 4
	// maxResolveAttempts bounds how often a command restarts because the
	// manager was Reset underneath it.
	maxResolveAttempts = 3
)

var errManagerReset = errors.New("ptz: manager was reset")

// Manager caches ONVIF clients and capabilities per camera, serialises the
// commands sent to each camera, and makes sure a camera that was told to move
// is always told to stop again: every Move arms a per-camera safety timer
// that sends Stop unless it is re-armed by another Move or cancelled by Stop.
//
// All methods are safe for concurrent use.
type Manager struct {
	log *slog.Logger
	hc  *http.Client

	// Durations; unexported so tests can shrink them.
	probeTimeout   time.Duration // bound on one capability probe (Dial)
	cmdTimeout     time.Duration // bound on one Move/Stop (and on waiting for the camera lock)
	maxMove        time.Duration // how long a Move lasts without being renewed
	capsTTL        time.Duration // how long a successful probe is trusted
	failureTTL     time.Duration // how long a failed probe is remembered
	authFailureTTL time.Duration // how long a probe rejected for credentials is remembered
	stopRetryDelay time.Duration // delay before re-sending a failed safety Stop

	wg sync.WaitGroup // pending safety timers and background work

	mu      sync.Mutex
	entries map[string]*entry
	// locks holds each camera id's command lock for the lifetime of the
	// Manager. It deliberately survives Reset and Target changes: a retired
	// entry's last Stop and its replacement's first Move must still exclude
	// each other, so every entry of one camera id shares one lock.
	locks map[string]chan struct{}

	// failing holds the camera ids whose last recorded probe failed, so the
	// log carries one line per transition rather than one per probe. Like
	// locks it survives Reset: a config reload that probes a still-broken
	// camera again must not repeat the warning.
	healthMu sync.Mutex
	failing  map[string]bool
}

// NewManager returns a Manager with production timeouts. A nil logger
// discards logs.
func NewManager(logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	return &Manager{
		log:            logger,
		hc:             &http.Client{Transport: tr, Timeout: defaultHTTPTimeout, CheckRedirect: noRedirect},
		probeTimeout:   defaultProbeTimeout,
		cmdTimeout:     defaultCommandTimeout,
		maxMove:        defaultMaxMove,
		capsTTL:        defaultCapsTTL,
		failureTTL:     defaultFailureTTL,
		authFailureTTL: defaultAuthFailureTTL,
		stopRetryDelay: defaultStopRetryDelay,
		entries:        map[string]*entry{},
		locks:          map[string]chan struct{}{},
		failing:        map[string]bool{},
	}
}

// entry is the manager's state for one camera at one resolved Target. A
// changed Target (or Reset) retires the entry and starts a fresh one.
type entry struct {
	id     string
	target Target
	// cmd is the camera's one-slot command semaphore, shared by every entry of
	// this camera id (see Manager.locks); unlike a sync.Mutex it can be waited
	// on with a context.
	cmd chan struct{}

	ctx    context.Context // cancelled when the entry is retired; parents probes
	cancel context.CancelFunc

	mu     sync.Mutex // guards everything below; a leaf lock
	dead   bool
	client *Client // last successfully dialled client; kept past expiry so a Stop can always be sent
	res    *outcome
	probe  *probeCall

	// Safety timer. gen identifies the arming a timer callback belongs to so
	// that a callback that lost the race against a newer Move or Stop does
	// nothing.
	gen       uint64
	armed     bool
	timer     *time.Timer
	armClient *Client
}

// outcome is a cached probe result.
type outcome struct {
	caps  Caps
	err   error
	until time.Time
}

// probeCall is one in-flight probe shared by every caller that needs it.
type probeCall struct {
	done   chan struct{}
	client *Client
	caps   Caps
	err    error
}

func (e *entry) lock(ctx context.Context) error {
	select {
	case e.cmd <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *entry) unlock() { <-e.cmd }

// fresh returns the cached outcome if it has not expired. e.mu must be held.
func (e *entry) fresh(now time.Time) *outcome {
	if e.res != nil && now.Before(e.res.until) {
		return e.res
	}
	return nil
}

func sameTarget(a, b Target) bool {
	return a.User == b.User && a.Pass == b.Pass && slices.Equal(a.Endpoints, b.Endpoints)
}

// entryFor returns the live entry for cam, retiring a stale one whose Target
// no longer matches.
func (m *Manager) entryFor(cam config.Camera) (*entry, error) {
	t, err := TargetFor(cam)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	old := m.entries[cam.ID]
	if old != nil && sameTarget(old.target, t) {
		m.mu.Unlock()
		return old, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	lock := m.locks[cam.ID]
	if lock == nil {
		lock = make(chan struct{}, 1)
		m.locks[cam.ID] = lock
	}
	e := &entry{id: cam.ID, target: t, cmd: lock, ctx: ctx, cancel: cancel}
	m.entries[cam.ID] = e
	m.mu.Unlock()
	if old != nil {
		m.retire(old)
	}
	return e, nil
}

// startProbeLocked returns the in-flight probe for e, starting one if there is
// none. e.mu must be held. Probes are detached from any caller's context (so
// a caller giving up neither wastes nor poisons the probe) and bounded by
// probeTimeout; the entry's context cancels them on Reset.
func (m *Manager) startProbeLocked(e *entry) *probeCall {
	if e.probe != nil {
		return e.probe
	}
	pc := &probeCall{done: make(chan struct{})}
	e.probe = pc
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(e.ctx, m.probeTimeout)
		defer cancel()
		c, err := Dial(ctx, e.target, m.hc)
		m.log.Debug("ptz probe", "camera", e.id, "ok", err == nil)

		e.mu.Lock()
		if err == nil {
			pc.client, pc.caps = c, c.Capabilities()
		} else {
			pc.err = err
		}
		// An outcome says something about the camera only if the probe ran to
		// its own conclusion: not if the entry was retired (Reset, changed
		// target) or the probe was otherwise cancelled.
		recorded := !e.dead && !errors.Is(err, context.Canceled)
		if recorded {
			o := &outcome{caps: pc.caps, err: pc.err, until: time.Now().Add(m.capsTTL)}
			if err != nil {
				// A rejected login is remembered for long: retrying it
				// every failure TTL could get the account or IP locked out.
				ttl := m.failureTTL
				if isAuthFailure(err) {
					ttl = m.authFailureTTL
				}
				o.until = time.Now().Add(ttl)
			} else {
				e.client = pc.client
			}
			e.res = o
		}
		if e.probe == pc {
			e.probe = nil
		}
		e.mu.Unlock()
		if recorded {
			m.noteProbe(e.id, err)
		}
		close(pc.done)
	}()
	return pc
}

// noteProbe logs probe health transitions for camera id: one Warn when it goes
// from not-failed (unknown, OK, or reachable without PTZ) to failed, one Info
// when it recovers, nothing for repeats. The log never carries the error text,
// which may name URLs or credentials; the class is the only detail.
func (m *Manager) noteProbe(id string, err error) {
	m.healthMu.Lock()
	was := m.failing[id]
	is := err != nil
	if is {
		m.failing[id] = true
	} else {
		delete(m.failing, id)
	}
	m.healthMu.Unlock()
	switch {
	case is && !was:
		m.log.Warn("ptz: probe failed", "camera", id, "class", failureClass(err))
	case !is && was:
		m.log.Info("ptz: probe recovered", "camera", id)
	}
}

// failureClass buckets a probe error: auth (the camera rejected our login),
// timeout, unreachable (could not connect) or protocol (anything else).
func failureClass(err error) string {
	var ne net.Error
	switch {
	case isAuthFailure(err):
		return "auth"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "timeout"
	case errors.As(err, new(*net.OpError)) || errors.As(err, new(*net.DNSError)):
		return "unreachable"
	}
	return "protocol"
}

// resolve returns the entry for cam together with its client and
// capabilities, probing (at most once concurrently) when nothing fresh is
// cached. It blocks for at most the probe timeout or until ctx ends.
func (m *Manager) resolve(ctx context.Context, cam config.Camera) (*entry, *Client, Caps, error) {
	for attempt := 0; ; attempt++ {
		e, err := m.entryFor(cam)
		if err != nil {
			return nil, nil, Caps{}, err
		}
		e.mu.Lock()
		if o := e.fresh(time.Now()); o != nil {
			c := e.client
			e.mu.Unlock()
			return e, c, o.caps, o.err
		}
		pc := m.startProbeLocked(e)
		e.mu.Unlock()

		select {
		case <-pc.done:
		case <-ctx.Done():
			// Nothing about the camera is learned from the caller giving up,
			// so nothing is cached; the probe carries on for other callers.
			return nil, nil, Caps{}, ctx.Err()
		}
		e.mu.Lock()
		dead := e.dead
		e.mu.Unlock()
		if dead && attempt+1 < maxResolveAttempts {
			continue // Reset mid-probe: the result belongs to a dropped entry
		}
		if dead {
			return nil, nil, Caps{}, errManagerReset
		}
		return e, pc.client, pc.caps, pc.err
	}
}

// CapsCached returns the camera's capabilities if they are known and fresh,
// without ever touching the network. A fresh cached failure is reported as
// (Caps{}, true): the camera is known to be unusable for now. If nothing fresh
// is cached it returns (Caps{}, false) and starts one background probe (none
// if one is already running) so a later call can succeed.
func (m *Manager) CapsCached(cam config.Camera) (Caps, bool) {
	e, err := m.entryFor(cam)
	if err != nil {
		return Caps{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if o := e.fresh(time.Now()); o != nil {
		return o.caps, true
	}
	m.startProbeLocked(e)
	return Caps{}, false
}

// Caps returns the camera's capabilities, probing it if nothing fresh is
// cached. A probe in flight is shared. It blocks for at most the probe
// timeout (or until ctx ends); on failure it returns Caps{} and the error,
// and the failure is cached for the failure TTL (or, when the camera rejected
// our login, for the much longer auth failure TTL, until Reset). A camera that answers but
// has no PTZ yields empty Caps and a nil error.
func (m *Manager) Caps(ctx context.Context, cam config.Camera) (Caps, error) {
	_, _, caps, err := m.resolve(ctx, cam)
	if err != nil {
		return Caps{}, err
	}
	return caps, nil
}

func supports(c Caps, d Direction) error {
	if c == (Caps{}) {
		return ErrNotPTZ
	}
	switch d {
	case Up, Down, Left, Right:
		if c.PanTilt {
			return nil
		}
	case ZoomIn, ZoomOut:
		if c.Zoom {
			return nil
		}
	}
	return ErrUnsupported
}

// Move starts moving the camera in direction d for at most maxMove and arms
// the safety timer that stops it then. The UI keeps a movement going by
// repeating Move while its button is held; each Move re-arms the timer.
func (m *Manager) Move(ctx context.Context, cam config.Camera, d Direction) error {
	if _, _, _, ok := d.velocity(); !ok {
		return ErrUnsupported
	}
	for attempt := 0; attempt < maxResolveAttempts; attempt++ {
		e, client, caps, err := m.resolve(ctx, cam)
		if err != nil {
			return err
		}
		if err := supports(caps, d); err != nil {
			return err
		}
		if err := m.lockCmd(ctx, e); err != nil {
			return err
		}
		e.mu.Lock()
		dead := e.dead
		e.mu.Unlock()
		if dead { // Reset while waiting for the lock: start over on the new entry
			e.unlock()
			continue
		}
		err = m.sendMove(ctx, e, client, d)
		e.unlock()
		return err
	}
	return errManagerReset
}

// sendMove sends the Move and arms the safety timer. The camera lock is held.
func (m *Manager) sendMove(ctx context.Context, e *entry, client *Client, d Direction) error {
	cctx, cancel := context.WithTimeout(ctx, m.cmdTimeout)
	err := client.Move(cctx, d, m.maxMove)
	cancel()
	m.log.Debug("ptz command", "camera", e.id, "action", "move", "ok", err == nil)

	// Arm even if the Move failed: the request may have reached the camera
	// (a timeout does not mean it was not processed), and a spurious Stop is
	// harmless where a missing one is not.
	e.mu.Lock()
	if e.dead {
		// Reset raced with us after its check; it found no timer to act on,
		// so the Stop is ours to send.
		e.mu.Unlock()
		m.stopHeld(e, client)
		return err
	}
	e.gen++
	m.armLocked(e, client, m.maxMove, e.gen, 0)
	e.mu.Unlock()
	return err
}

// Stop cancels the safety timer and stops the camera. It works from the last
// known client even if the capability cache has expired, so a Stop is never
// held up by a re-probe of a camera that already accepted a Move.
func (m *Manager) Stop(ctx context.Context, cam config.Camera) error {
	e, err := m.entryFor(cam)
	if err != nil {
		return err
	}
	e.mu.Lock()
	client := e.client
	e.mu.Unlock()
	if client == nil {
		if e, client, _, err = m.resolve(ctx, cam); err != nil {
			return err
		}
	}
	if client.Capabilities() == (Caps{}) {
		return ErrNotPTZ
	}
	if err := m.lockCmd(ctx, e); err != nil {
		return err
	}
	defer e.unlock()

	e.mu.Lock()
	wasArmed := e.armed
	e.gen++
	m.disarmLocked(e)
	e.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, m.cmdTimeout)
	defer cancel()
	err = client.Stop(cctx)
	m.log.Debug("ptz command", "camera", e.id, "action", "stop", "ok", err == nil)
	if err != nil && wasArmed {
		// The camera may still be moving and we just cancelled the timer that
		// would have stopped it: keep trying in the background.
		e.mu.Lock()
		if !e.dead {
			e.gen++
			m.armLocked(e, client, m.stopRetryDelay, e.gen, 1)
		}
		e.mu.Unlock()
	}
	return err
}

// lockCmd takes e's camera lock, waiting at most cmdTimeout.
func (m *Manager) lockCmd(ctx context.Context, e *entry) error {
	lctx, cancel := context.WithTimeout(ctx, m.cmdTimeout)
	defer cancel()
	return e.lock(lctx)
}

// armLocked (re)arms e's safety timer to send Stop after delay. e.mu must be
// held. attempt counts failed deliveries of this Stop so far.
func (m *Manager) armLocked(e *entry, client *Client, delay time.Duration, gen uint64, attempt int) {
	m.disarmLocked(e)
	e.armed = true
	e.armClient = client
	m.wg.Add(1)
	e.timer = time.AfterFunc(delay, func() {
		defer m.wg.Done()
		m.safetyStop(e, client, gen, attempt)
	})
}

// disarmLocked cancels e's timer. e.mu must be held.
func (m *Manager) disarmLocked(e *entry) {
	if e.timer != nil && e.timer.Stop() {
		m.wg.Done() // the callback will never run to release its slot
	}
	e.timer = nil
	e.armed = false
	e.armClient = nil
}

// safetyStop is the timer callback. It runs with no caller context: it takes
// the same camera lock as every command and sends Stop with a fresh timeout.
func (m *Manager) safetyStop(e *entry, client *Client, gen uint64, attempt int) {
	lctx, cancel := context.WithTimeout(context.Background(), m.cmdTimeout)
	err := e.lock(lctx)
	cancel()
	if err != nil {
		m.rearm(e, client, gen, attempt)
		return
	}
	defer e.unlock()

	e.mu.Lock()
	current := e.armed && e.gen == gen
	e.mu.Unlock()
	if !current {
		return // a newer Move or Stop (or retirement) owns the camera now
	}

	sctx, cancel := context.WithTimeout(context.Background(), m.cmdTimeout)
	err = client.Stop(sctx)
	cancel()
	m.log.Debug("ptz command", "camera", e.id, "action", "safety_stop", "ok", err == nil)
	if err == nil {
		e.mu.Lock()
		if e.armed && e.gen == gen {
			e.timer, e.armed, e.armClient = nil, false, nil
		}
		e.mu.Unlock()
		return
	}
	m.rearm(e, client, gen, attempt)
}

// rearm schedules another delivery attempt of a safety Stop that failed, unless
// the attempts are used up or something newer superseded this Stop.
func (m *Manager) rearm(e *entry, client *Client, gen uint64, attempt int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.armed || e.gen != gen || e.dead {
		return
	}
	if attempt+1 >= maxStopAttempts {
		e.timer, e.armed, e.armClient = nil, false, nil
		m.log.Warn("ptz safety stop failed; giving up", "camera", e.id)
		return
	}
	m.armLocked(e, client, m.stopRetryDelay, gen, attempt+1)
}

// stopFresh takes the camera lock and sends one Stop on a fresh context.
func (m *Manager) stopFresh(e *entry, client *Client) {
	lctx, cancel := context.WithTimeout(context.Background(), m.cmdTimeout)
	err := e.lock(lctx)
	cancel()
	if err != nil {
		m.log.Warn("ptz stop skipped: camera busy", "camera", e.id)
		return
	}
	defer e.unlock()
	m.stopHeld(e, client)
}

// stopHeld sends one Stop on a fresh context. The camera lock must be held.
func (m *Manager) stopHeld(e *entry, client *Client) {
	sctx, cancel := context.WithTimeout(context.Background(), m.cmdTimeout)
	defer cancel()
	err := client.Stop(sctx)
	m.log.Debug("ptz command", "camera", e.id, "action", "stop_on_retire", "ok", err == nil)
}

// retire kills e: cancels its probe, and if its camera was left moving sends
// the Stop that the cancelled timer would have.
func (m *Manager) retire(e *entry) {
	e.mu.Lock()
	e.dead = true
	e.cancel()
	moving, client := e.armed, e.armClient
	m.disarmLocked(e)
	e.mu.Unlock()
	if moving && client != nil {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.stopFresh(e, client)
		}()
	}
}

// Reset drops every cached client and capability and cancels the safety
// timers (for example on config reload). A camera that was still moving is
// sent a Stop in the background rather than left to run on.
func (m *Manager) Reset() {
	m.mu.Lock()
	old := m.entries
	m.entries = map[string]*entry{}
	m.mu.Unlock()
	for _, e := range old {
		m.retire(e)
	}
	m.hc.CloseIdleConnections()
}
