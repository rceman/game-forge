package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rceman/game-forge/internal/core"
	"github.com/rceman/game-forge/internal/op"
	"github.com/rceman/game-forge/schemas"
)

// HousekeepingInterval is the cadence of the in-process housekeeping loop.
const HousekeepingInterval = 15 * time.Second

// Server is the control daemon. It serves the Operation Registry over
// loopback HTTP and owns the housekeeping loop.
type Server struct {
	rt      *core.Runtime
	reg     *op.Registry
	token   string
	logger  *log.Logger
	done    chan struct{}
	stopCh  chan struct{}
	once    bool
	serveFn func(ln net.Listener, h http.Handler) error
}

// NewServer builds a daemon around a Runtime and registry.
func NewServer(rt *core.Runtime, reg *op.Registry, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &Server{
		rt:     rt,
		reg:    reg,
		logger: logger,
		done:   make(chan struct{}),
		stopCh: make(chan struct{}),
	}
}

// Serve binds a dynamic loopback port, writes discovery state, runs startup
// reconciliation and the housekeeping loop, and serves until stopped.
func (s *Server) Serve() error {
	tok, err := newToken()
	if err != nil {
		return err
	}
	s.token = tok
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("bind daemon: %w", err)
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

// routes returns the authenticated HTTP mux.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.auth(s.handleHealth))
	mux.HandleFunc("/v1/capabilities", s.auth(s.handleCapabilities))
	mux.HandleFunc("/v1/schema/", s.auth(s.handleSchema))
	mux.HandleFunc("/v1/run", s.auth(s.handleRun))
	mux.HandleFunc("/v1/shutdown", s.auth(s.handleShutdown))
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
	o, ok := s.reg.Lookup(req.Op)
	if !ok {
		writeErr(w, http.StatusNotFound, &op.Error{Code: op.CodeUnknownOp, Msg: "unknown operation " + req.Op})
		return
	}
	if verr := o.ValidateArgs(req.Args); verr != nil {
		writeErr(w, http.StatusBadRequest, verr)
		return
	}

	ctx := op.WithCwd(r.Context(), req.Cwd)
	stream := strings.Contains(r.Header.Get("Accept"), "application/x-ndjson")
	if stream {
		s.streamRun(w, ctx, req, o)
		return
	}
	s.simpleRun(w, ctx, req, o)
}

// simpleRun executes an operation and returns one compact reply.
//
// A handler may return a result and a failure together: the operation ran, its
// output is meaningful, but it asserts a failure. Both are sent.
func (s *Server) simpleRun(w http.ResponseWriter, ctx context.Context, req op.Request, o *op.Operation) {
	data, err := o.Handler(ctx, req.Args, op.NopSink{})
	resp := op.Response{ID: req.ID, OK: err == nil}
	if data != nil {
		if verr := o.ValidateOutput(data); verr != nil {
			resp.OK = false
			resp.Err = verr
		} else {
			resp.Data = data
		}
	}
	if err != nil && resp.Err == nil {
		resp.Err = toWireErr(err)
	}
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

	runID := "r1"
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
	data, err := o.Handler(ctx, req.Args, sink)
	done := op.Event{Ev: op.EvDone, Run: runID}
	code := 0
	if data != nil {
		if verr := o.ValidateOutput(data); verr != nil {
			done.Err = verr
			code = 1
		} else {
			done.Data = data
		}
	}
	if err != nil && done.Err == nil {
		done.Err = toWireErr(err)
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
