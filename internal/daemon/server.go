package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rceman/game-forge/internal/core"
	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/internal/project"
	"github.com/rceman/game-forge/schemas"
)

// HousekeepingInterval is the cadence of the in-process housekeeping loop.
const HousekeepingInterval = 15 * time.Second

// Server is the control daemon. It serves the Operation Registry and the MCP
// endpoint over one loopback HTTP listener on the durable daemon port, and
// owns the housekeeping loop.
type Server struct {
	rt          *core.Runtime
	reg         *op.Registry
	token       string
	logger      *log.Logger
	done        chan struct{}
	stopCh      chan struct{}
	once        bool
	incarnation string
	runSeq      atomic.Int64
	serveFn     func(ln net.Listener, h http.Handler) error

	// projects is the durable machine-local project registry. Resolution is
	// read-on-request so CLI registration is visible without restart.
	projects *project.Registry
	projErr  error
	// mcpTok is the durable MCP credential for /mcp (distinct from the
	// ephemeral per-incarnation daemon token above).
	mcpTok string
	mcpErr error
	// mcpH is the streamable-HTTP MCP handler, injected at startup by the
	// caller that wires the frontend (cli's daemon serve) — the daemon
	// package itself stays free of the frontend, avoiding an import cycle.
	mcpH http.Handler
}

// NewServer builds a daemon around a Runtime and registry.
func NewServer(rt *core.Runtime, reg *op.Registry, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	tok, _ := newToken()
	proj, perr := project.OpenRegistry()
	mcpTok, mcpErr := DurableMCPToken()
	return &Server{
		rt:          rt,
		reg:         reg,
		logger:      logger,
		token:       tok,
		done:        make(chan struct{}),
		stopCh:      make(chan struct{}),
		incarnation: newIncarnation(),
		projects:    proj,
		projErr:     perr,
		mcpTok:      mcpTok,
		mcpErr:      mcpErr,
	}
}

// newIncarnation returns a short random identity for this daemon process. It
// is NOT derived from the bearer token; run ids must never carry token
// material.
func newIncarnation() string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", os.Getpid()&0xffff)
	}
	return hex.EncodeToString(b)
}

// nextID returns a compact, per-incarnation-unique id, e.g. "r_a1b2c3_4".
// Concurrent streams never share one, and ids are unique across restarts
// because the incarnation differs.
func (s *Server) nextID(prefix string) string {
	return fmt.Sprintf("%s_%s_%d", prefix, s.incarnation, s.runSeq.Add(1))
}

// ownershipWait bounds how long a new incarnation waits for a previous daemon
// to finish draining (resource release happens after the listener closes, so
// a predecessor can hold ownership while already unreachable). It is a var so
// tests can shrink it.
var ownershipWait = 35 * time.Second

// acquireLifetimeLock claims daemon ownership for this process's lifetime.
// The file carries the owning pid so a crashed daemon's stale lock is
// recovered rather than waited on forever. A live holder means a predecessor
// is either healthy or mid-shutdown — the worker waits for it to relinquish
// ownership instead of racing a half-drained daemon.
func acquireLifetimeLock() (func(), error) {
	path, err := OwnedLockPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(ownershipWait)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			return func() {
				f.Close()
				_ = os.Remove(path)
			}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		// The lock exists: a dead owner leaves a stale file we reclaim; a live
		// one is draining and we wait for it to release ownership.
		data, _ := os.ReadFile(path)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid <= 0 || !processAlive(pid) {
			_ = os.Remove(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for game-forged (pid %d) to release ownership", pid)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// OwnershipFree reports whether no daemon incarnation currently holds the
// lifetime ownership lock — the authoritative "previous daemon is gone"
// signal for stop/restart, since the endpoint closes before resource drain
// completes.
func OwnershipFree() bool {
	path, err := OwnedLockPath()
	if err != nil {
		return true
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return pid <= 0 || !processAlive(pid)
}

// Serve binds the durable loopback port, writes discovery state, runs startup
// reconciliation and the housekeeping loop, and serves until stopped.
func (s *Server) Serve() error {
	// Only one daemon incarnation may own discovery/control at once. The
	// lifetime lock is enforced by the worker itself, not just by the CLI's
	// startup coordination, so two directly launched serves converge.
	release, err := acquireLifetimeLock()
	if err != nil {
		return err
	}
	defer release()
	if s.token == "" {
		tok, err := newToken()
		if err != nil {
			return err
		}
		s.token = tok
	}
	tok := s.token
	// The listener binds the durable port: first start picks an available
	// 50000-59999 port and persists it; restarts rebind the exact same port so
	// a configured MCP endpoint stays valid. An occupied persisted port is a
	// clear failure resolved by `daemon rebind`, never a silent move.
	ln, _, err := bindEndpoint()
	if err != nil {
		return err
	}
	disc := &Discovery{
		Protocol: Protocol,
		Endpoint: "http://" + ln.Addr().String(),
		PID:      os.Getpid(),
		Token:    tok,
	}
	if err := Write(disc); err != nil {
		ln.Close()
		return fmt.Errorf("write discovery: %w", err)
	}
	defer Remove()

	// Startup reconciliation: reclaim resources left expired by a previous
	// incarnation before serving.
	s.reconcile()

	go s.housekeeping()

	// Service managers stop the daemon with SIGTERM (systemd stop/restart);
	// map it to the same graceful shutdown as /v1/shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, stopSignals()...)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			s.Stop()
		case <-s.done:
		}
	}()

	srv := &http.Server{Handler: s.routes()}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Serve(ln)
	}()
	s.logger.Printf("game-forged listening %s pid=%d", disc.Endpoint, disc.PID)

	var graceful bool
	select {
	case <-s.stopCh:
		graceful = true
	case err := <-errCh:
		close(s.done)
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	// Hold the startup lock while releasing owned resources so a concurrent
	// starter waits for the release to finish rather than spawning a new daemon
	// against a half-drained registry.
	releaseLock := s.holdStartupLock()
	_ = Remove()
	close(s.done)
	if graceful {
		s.releaseOwned()
	}
	if releaseLock != nil {
		releaseLock()
	}
	return nil
}

// holdStartupLock attempts to acquire the startup lock. It returns a release
// function, or nil when another starter already holds it.
func (s *Server) holdStartupLock() func() {
	path, err := LockPath()
	if err != nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return func() {
		f.Close()
		os.Remove(path)
	}
}

// releaseOwned reclaims the resources the daemon owns so a stopped daemon
// leaves nothing behind.
func (s *Server) releaseOwned() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := core.New(s.rt, "")
	if err != nil {
		return
	}
	defer c.Close()
	if res, err := c.ReleaseOwned(ctx); err == nil && len(res.Reclaimed) > 0 {
		s.logger.Printf("shutdown: released %d owned resources", len(res.Reclaimed))
	}
}

// Stop requests a graceful shutdown.
func (s *Server) Stop() {
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}

// reconcile performs one housekeeping pass at startup.
func (s *Server) reconcile() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := core.New(s.rt, "")
	if err != nil {
		return
	}
	defer c.Close()
	if res, err := c.Tick(ctx); err == nil && len(res.Reclaimed) > 0 {
		s.logger.Printf("startup: reclaimed %d expired resources", len(res.Reclaimed))
	}
}

// housekeeping periodically invokes the same Tick Core logic as
// "game-forge tick". It never shells out to the binary.
func (s *Server) housekeeping() {
	t := time.NewTicker(HousekeepingInterval)
	defer t.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			c, err := core.New(s.rt, "")
			if err == nil {
				if res, err := c.Tick(ctx); err == nil && len(res.Reclaimed) > 0 {
					s.logger.Printf("housekeeping: reclaimed %d expired resources", len(res.Reclaimed))
				}
				c.Close()
			}
			cancel()
		}
	}
}

// Token returns the bearer token for this incarnation. It is used by tests
// serving the handler directly.
func (s *Server) Token() string { return s.token }

// Handler returns the daemon's authenticated HTTP handler. It is exported so
// tests and frontends can serve the same routes without binding a listener.
func (s *Server) Handler() http.Handler { return s.routes() }

// routes returns the authenticated HTTP mux. /mcp uses the separate durable
// MCP credential, not the ephemeral daemon token — the two auth domains never
// substitute for one another.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.auth(s.handleHealth))
	mux.HandleFunc("/v1/capabilities", s.auth(s.handleCapabilities))
	mux.HandleFunc("/v1/catalog", s.auth(s.handleCatalog))
	mux.HandleFunc("/v1/schema/", s.auth(s.handleSchema))
	mux.HandleFunc("/v1/run", s.auth(s.handleRun))
	mux.HandleFunc("/v1/shutdown", s.auth(s.handleShutdown))
	mux.Handle("/mcp", s.mcpAuth(s.mcpHandler()))
	return mux
}

// auth enforces the bearer token on every endpoint.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") || h[len("Bearer "):] != s.token {
			writeErr(w, http.StatusUnauthorized, &op.Error{Code: "unauthorized", Msg: "missing or invalid bearer token"})
			return
		}
		next(w, r)
	}
}

// mcpAuth enforces the durable MCP credential on /mcp and rejects browser
// requests whose Origin is not local. Token bytes never appear in replies.
func (s *Server) mcpAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !localOrigin(r.Header.Get("Origin")) {
			writeErr(w, http.StatusForbidden, &op.Error{Code: "forbidden", Msg: "non-local Origin rejected"})
			return
		}
		h := r.Header.Get("Authorization")
		if s.mcpErr != nil || s.mcpTok == "" ||
			!strings.HasPrefix(h, "Bearer ") || h[len("Bearer "):] != s.mcpTok {
			writeErr(w, http.StatusUnauthorized, &op.Error{Code: "unauthorized", Msg: "missing or invalid MCP credential"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// MCPToken returns the durable MCP credential. The endpoint advertises it
// only through explicit commands like `mcp info --show-token`.
func (s *Server) MCPToken() string { return s.mcpTok }

// SetMCP mounts the streamable-HTTP MCP handler. The frontend is built by the
// caller (the daemon's own serve command) around this server as dispatcher, so
// MCP calls execute the same canonical pipeline as /v1/run with no HTTP loop
// into the daemon itself.
func (s *Server) SetMCP(h http.Handler) { s.mcpH = h }

// mcpHandler returns the mounted MCP handler, or a clear 503 when the daemon
// was built without one (tests that only exercise the control API).
func (s *Server) mcpHandler() http.Handler {
	if s.mcpH == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeErr(w, http.StatusServiceUnavailable, &op.Error{Code: op.CodeInternal, Msg: "MCP frontend not mounted"})
		})
	}
	return s.mcpH
}

// Catalog returns the in-process operation catalog — it satisfies the MCP
// frontend's dispatcher contract without this package importing it.
func (s *Server) Catalog(_ context.Context) (string, int, []op.Meta, error) {
	ops := make([]op.Meta, 0, s.reg.Len())
	for _, o := range s.reg.All() {
		ops = append(ops, op.Meta{
			Op:      o.Name,
			Summary: o.Summary,
			Stream:  o.Stream,
			Input:   o.InputSchema(),
			Output:  o.OutputSchema(),
		})
	}
	return Protocol, schemas.Version, ops, nil
}

// Run is the in-process dispatch path for MCP calls — it satisfies the MCP
// frontend's dispatcher contract and shares /v1/run's prepare+exec pipeline:
// same project resolution, validation, run ids, cancellation, progress and
// output checks.
func (s *Server) Run(ctx context.Context, req *op.Request, sink op.Sink) (any, *op.Error) {
	o, perr := s.prepare(req)
	if perr != nil {
		return nil, perr
	}
	ctx = op.WithRunID(op.WithCwd(ctx, req.Cwd), s.nextID("r"))
	return s.exec(ctx, req, o, sink)
}

// Requests satisfies the dispatcher contract's audit hook: in-process
// dispatch makes no HTTP round trips.
func (s *Server) Requests() int64 { return 0 }

// handleHealth reports liveness.
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"v":        schemas.Version,
		"protocol": Protocol,
		"pid":      os.Getpid(),
	})
}

// handleCapabilities lists the registered operations.
func (s *Server) handleCapabilities(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"v":   schemas.Version,
		"ops": s.reg.Names(),
	})
}

// handleCatalog returns every operation's full contract in ONE response so a
// frontend (the MCP server) can initialize without N per-schema round trips.
// The per-operation /v1/schema/<op> endpoint remains for debugging.
func (s *Server) handleCatalog(w http.ResponseWriter, _ *http.Request) {
	ops := make([]map[string]any, 0, s.reg.Len())
	for _, o := range s.reg.All() {
		var in, out any
		_ = json.Unmarshal(o.InputSchema(), &in)
		_ = json.Unmarshal(o.OutputSchema(), &out)
		ops = append(ops, map[string]any{
			"op":      o.Name,
			"summary": o.Summary,
			"stream":  o.Stream,
			"input":   in,
			"output":  out,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"v":        schemas.Version,
		"protocol": Protocol,
		"ops":      ops,
	})
}

// handleSchema returns an operation's canonical contract.
func (s *Server) handleSchema(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/v1/schema/")
	o, ok := s.reg.Lookup(name)
	if !ok {
		writeErr(w, http.StatusNotFound, &op.Error{Code: op.CodeUnknownOp, Msg: "unknown operation " + name})
		return
	}
	var in, out any
	_ = json.Unmarshal(o.InputSchema(), &in)
	_ = json.Unmarshal(o.OutputSchema(), &out)
	writeJSON(w, http.StatusOK, map[string]any{
		"op":      o.Name,
		"summary": o.Summary,
		"stream":  o.Stream,
		"input":   in,
		"output":  out,
	})
}

// handleShutdown gracefully stops the daemon.
func (s *Server) handleShutdown(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.Stop()
	}()
}

// handleRun dispatches one operation request.
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, &op.Error{Code: op.CodeInvalidRequest, Msg: "POST required"})
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, &op.Error{Code: op.CodeInvalidRequest, Msg: "read body: " + err.Error()})
		return
	}
	var env any
	if err := json.Unmarshal(raw, &env); err != nil {
		writeErr(w, http.StatusBadRequest, &op.Error{Code: op.CodeInvalidRequest, Msg: "malformed JSON"})
		return
	}
	if verr := op.ValidateRequest(env); verr != nil {
		writeErr(w, http.StatusBadRequest, verr)
		return
	}
	var req op.Request
	_ = json.Unmarshal(raw, &req)
	if req.V != schemas.Version {
		writeErr(w, http.StatusBadRequest, &op.Error{Code: op.CodeUnsupported, Msg: fmt.Sprintf("unsupported version %d", req.V)})
		return
	}
	o, perr := s.prepare(&req)
	if perr != nil {
		writeErr(w, statusForErr(perr), perr)
		return
	}
	// A request id is never ambiguously empty: when the client omits one, the
	// daemon assigns a unique "q_<inc>_<n>" so replies/events always carry a
	// correlation identifier.
	if req.ID == "" {
		req.ID = s.nextID("q")
	}

	ctx := op.WithCwd(r.Context(), req.Cwd)
	stream := strings.Contains(r.Header.Get("Accept"), "application/x-ndjson")
	if stream {
		s.streamRun(w, ctx, req, o)
		return
	}
	// A non-streamed run still gets a unique run id so the resources it owns
	// group under a daemon-correlatable identity.
	s.simpleRun(w, op.WithRunID(ctx, s.nextID("r")), req, o)
}

// prepare resolves the request's project selector and validates args. It is
// the shared front half of dispatch used by /v1/run and the in-process MCP
// dispatcher: a "project" code resolves against the durable registry to the
// registered canonical root; exactly one of project/cwd may select context.
func (s *Server) prepare(req *op.Request) (*op.Operation, *op.Error) {
	o, ok := s.reg.Lookup(req.Op)
	if !ok {
		return nil, &op.Error{Code: op.CodeUnknownOp, Msg: "unknown operation " + req.Op}
	}
	if req.Project != "" {
		if req.Cwd != "" {
			return nil, &op.Error{Code: op.CodeInvalidRequest, Msg: "specify project or cwd, not both"}
		}
		if s.projErr != nil {
			return nil, &op.Error{Code: op.CodeInternal, Msg: "project registry: " + s.projErr.Error()}
		}
		root, lerr := s.projects.Resolve(req.Project)
		if lerr != nil {
			return nil, &op.Error{Code: lerr.Code, Msg: lerr.Msg}
		}
		req.Cwd = root
		req.Project = ""
	}
	if verr := o.ValidateArgs(req.Args); verr != nil {
		return nil, verr
	}
	return o, nil
}

// exec invokes the handler and validates its output — the shared back half of
// dispatch. A handler may return data and a failure together: the output is
// meaningful but asserts a failure; both are reported.
func (s *Server) exec(ctx context.Context, req *op.Request, o *op.Operation, sink op.Sink) (any, *op.Error) {
	data, err := o.Handler(ctx, req.Args, sink)
	var werr *op.Error
	if data != nil {
		if verr := o.ValidateOutput(data); verr != nil {
			data = nil
			werr = verr
		}
	}
	if err != nil && werr == nil {
		werr = toWireErr(err)
	}
	return data, werr
}

// statusForErr maps a pre-dispatch error to an HTTP status.
func statusForErr(e *op.Error) int {
	switch e.Code {
	case op.CodeUnknownOp:
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}

// simpleRun executes an operation and returns one compact reply.
//
// A handler may return a result and a failure together: the operation ran, its
// output is meaningful, but it asserts a failure. Both are sent.
func (s *Server) simpleRun(w http.ResponseWriter, ctx context.Context, req op.Request, o *op.Operation) {
	data, werr := s.exec(ctx, &req, o, op.NopSink{})
	resp := op.Response{ID: req.ID, OK: werr == nil, Data: data, Err: werr}
	if verr := op.ValidateResponse(resp); verr != nil {
		resp = op.Response{ID: req.ID, Err: &op.Error{Code: op.CodeInternal, Msg: "invalid response"}}
	}
	writeJSON(w, statusFor(resp), resp)
}

// streamRun executes an operation and streams NDJSON events.
func (s *Server) streamRun(w http.ResponseWriter, ctx context.Context, req op.Request, o *op.Operation) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	fl, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)

	runID := s.nextID("r")
	emit := func(ev op.Event) {
		if ev.ID == "" {
			ev.ID = req.ID
		}
		if verr := op.ValidateEvent(ev); verr != nil {
			code := 1
			ev = op.Event{ID: req.ID, Ev: op.EvDone, Status: op.StatusFail, Code: &code, Err: &op.Error{Code: op.CodeInternal, Msg: "invalid event"}}
		}
		_ = enc.Encode(ev)
		if fl != nil {
			fl.Flush()
		}
	}
	sink := &eventSink{emit: emit, run: runID}
	emit(op.Event{Ev: op.EvStart, Run: runID})
	data, werr := s.exec(op.WithRunID(ctx, runID), &req, o, sink)
	done := op.Event{Ev: op.EvDone, Run: runID, Data: data, Err: werr}
	code := 0
	if werr != nil {
		code = 1
	}
	if code != 0 {
		done.Status = op.StatusFail
	} else {
		done.Status = op.StatusPass
	}
	done.Code = &code
	emit(done)
}

// eventSink translates Core progress into NDJSON events.
type eventSink struct {
	emit func(op.Event)
	run  string
}

// Stage emits a stage event.
func (e *eventSink) Stage(name, status string, ms int64, detail string) {
	e.emit(op.Event{Ev: op.EvStage, Run: e.run, Name: name, Status: status, MS: ms, Data: detail})
}

// Artifact emits an artifact reference event.
func (e *eventSink) Artifact(kind, ref, path string) {
	e.emit(op.Event{Ev: op.EvArtifact, Run: e.run, Kind: kind, Ref: ref, Path: path})
}

// toWireErr converts an error into the structured wire error.
func toWireErr(err error) *op.Error {
	var e *op.Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &op.Error{Code: op.CodeCanceled, Msg: err.Error()}
	}
	return &op.Error{Code: op.CodeFailed, Msg: err.Error()}
}

// statusFor maps a reply to an HTTP status.
func statusFor(resp op.Response) int {
	if resp.OK {
		return http.StatusOK
	}
	if resp.Err != nil {
		switch resp.Err.Code {
		case op.CodeInvalidArgs, op.CodeInvalidRequest:
			return http.StatusBadRequest
		case op.CodeUnknownOp:
			return http.StatusNotFound
		}
	}
	return http.StatusInternalServerError
}

// writeJSON writes a compact JSON reply.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr writes a structured error reply.
func writeErr(w http.ResponseWriter, status int, e *op.Error) {
	writeJSON(w, status, op.Response{OK: false, Err: e})
}
