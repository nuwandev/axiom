package api

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nuwandev/axiom/internal/audit"
	"github.com/nuwandev/axiom/internal/jobs"
)

// actionNamePattern mirrors config's action-name charset (see
// internal/config.actionNamePattern) so a malformed action segment in the
// URL is rejected with a clean 400 before it ever reaches a map lookup,
// audit record, or log line.
var actionNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

// maxParametersPerRequest and maxParameterValueLen are request-level caps
// enforced before a request ever reaches the job manager — defense in
// depth alongside (not a replacement for) each action's own declared
// parameter schema in config, which is authoritative for what an action
// actually accepts.
const (
	maxParametersPerRequest = 64
	maxParameterValueLen    = 4096
)

type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

const maxRequestBodyBytes = 64 * 1024

// retryAfterSeconds rounds a wait up to whole seconds for the Retry-After
// header; HTTP carries no fractions and "0" would invite an immediate retry.
func retryAfterSeconds(d time.Duration) int {
	return int(math.Max(1, math.Ceil(d.Seconds())))
}

type triggerRequest struct {
	Parameters map[string]string `json:"parameters"`
}

// handleTriggerAction implements POST /v1/actions/{action}. Authorization
// (identity -> allowed action, default-deny) is enforced here, not just
// authentication in the middleware.
func (s *Server) handleTriggerAction(w http.ResponseWriter, r *http.Request) {
	identity := identityFromContext(r.Context())
	if identity == nil {
		s.rejectUnauthenticated(w, r, "missing identity for non-anonymous route")
		return
	}
	action := r.PathValue("action")
	if !actionNamePattern.MatchString(action) {
		writeError(w, http.StatusBadRequest, "invalid action name")
		return
	}

	allowed := s.cfg.Load().Identities[identity.CommonName]
	if !allowed.IsAllowed(action) {
		if err := s.audit.Write(audit.Record{
			Event:    audit.EventRejected,
			Identity: identity.CommonName,
			Action:   action,
			Reason:   "unauthorized: action not allowlisted for identity",
		}); err != nil {
			s.logger.Error("audit write failed for rejected request", "error", err)
		}
		writeError(w, http.StatusForbidden, "action not permitted for this identity")
		return
	}

	var req triggerRequest
	if r.ContentLength != 0 {
		ct := r.Header.Get("Content-Type")
		if ct != "" && !strings.HasPrefix(ct, "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
			return
		}
		body := http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		dec := json.NewDecoder(body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusBadRequest, "request body too large")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		// Exactly one JSON value: reject trailing data rather than silently
		// ignoring it (it could be a second, differently-parsed document).
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid JSON body: unexpected data after the JSON object")
			return
		}
	}

	if len(req.Parameters) > maxParametersPerRequest {
		writeError(w, http.StatusBadRequest, "too many parameters")
		return
	}
	for name, value := range req.Parameters {
		if len(value) > maxParameterValueLen {
			writeError(w, http.StatusBadRequest, "parameter value too large: "+name)
			return
		}
	}

	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}

	var (
		jobID    string
		replayed bool
		err      error
	)
	if key == "" {
		var job *jobs.Job
		if job, err = s.jobs.Trigger(r.Context(), action, identity.CommonName, req.Parameters); err == nil {
			jobID = job.ID
		}
	} else {
		jobID, replayed, err = s.jobs.TriggerIdempotent(r.Context(), action, identity.CommonName, key, req.Parameters)
	}
	if err != nil {
		switch {
		case errors.Is(err, jobs.ErrIdempotencyKeyReused):
			writeError(w, http.StatusUnprocessableEntity, err.Error())
		case errors.Is(err, jobs.ErrIdempotencyStoreFull):
			w.Header().Set("Retry-After", "10")
			writeError(w, http.StatusTooManyRequests, err.Error())
		case errors.Is(err, jobs.ErrUnknownAction):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, jobs.ErrUnknownParameter), errors.Is(err, jobs.ErrInvalidParameter), errors.Is(err, jobs.ErrMissingParameter):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, jobs.ErrActionBusy):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, jobs.ErrRateLimited):
			var rl *jobs.RateLimitError
			errors.As(err, &rl)
			w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(rl.RetryAfter)))
			writeError(w, http.StatusTooManyRequests, err.Error())
		case errors.Is(err, jobs.ErrTooManyJobs):
			w.Header().Set("Retry-After", "10")
			writeError(w, http.StatusTooManyRequests, err.Error())
		case errors.Is(err, jobs.ErrShuttingDown):
			writeError(w, http.StatusServiceUnavailable, err.Error())
		default:
			s.logger.Error("triggering action failed", "action", action, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to start action")
		}
		return
	}

	status := jobs.StatusQueued
	if replayed {
		// The key was already used: nothing was started. Report the original
		// job as it is now, not as "queued".
		snap, found := s.jobs.Get(jobID)
		if !found {
			// The job aged out of the history while its key was still valid.
			// Running the action again would defeat the key, so say so.
			writeError(w, http.StatusConflict, "this idempotency key already started job "+jobID+", whose record is no longer retained")
			return
		}
		status = snap.Status
		w.Header().Set("Idempotent-Replay", "true")
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"job_id": jobID,
		"status": string(status),
	})
}

// idempotencyKeyPattern bounds the key to a small, safe charset: it is
// client-controlled and used as a map key and in logs.
var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// idempotencyKey returns the request's Idempotency-Key, or "" when it has
// none. A malformed or repeated header is a 400 rather than being ignored:
// ignoring it would let the client believe it had retry protection it does
// not have. On failure it has already written the response.
func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values("Idempotency-Key")
	switch {
	case len(values) == 0:
		return "", true
	case len(values) > 1:
		writeError(w, http.StatusBadRequest, "Idempotency-Key must be given once")
		return "", false
	case !idempotencyKeyPattern.MatchString(values[0]):
		writeError(w, http.StatusBadRequest, "Idempotency-Key must be 1-128 characters from A-Z a-z 0-9 . _ : -")
		return "", false
	}
	return values[0], true
}

// ownedJob resolves {job_id} for the authenticated caller. A job is visible
// only to the identity that triggered it: any other caller, and any unknown
// or malformed ID, gets the same 404 so job IDs cannot be probed. On failure
// it has already written the response and returns ok=false.
func (s *Server) ownedJob(w http.ResponseWriter, r *http.Request) (jobs.Snapshot, bool) {
	identity := identityFromContext(r.Context())
	if identity == nil {
		s.rejectUnauthenticated(w, r, "missing identity")
		return jobs.Snapshot{}, false
	}

	jobID := r.PathValue("job_id")
	if !jobs.ValidID(jobID) {
		writeError(w, http.StatusNotFound, "job not found")
		return jobs.Snapshot{}, false
	}
	snap, ok := s.jobs.Get(jobID)
	if !ok || snap.RequestedBy != identity.CommonName {
		writeError(w, http.StatusNotFound, "job not found")
		return jobs.Snapshot{}, false
	}
	return snap, true
}

type jobResponse struct {
	JobID      string `json:"job_id"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// handleGetJob implements GET /v1/jobs/{job_id}.
func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	snap, ok := s.ownedJob(w, r)
	if !ok {
		return
	}

	resp := jobResponse{
		JobID:      snap.ID,
		Action:     snap.Action,
		Status:     string(snap.Status),
		ExitCode:   snap.ExitCode,
		Error:      snap.Error,
		DurationMs: snap.DurationMs,
	}
	if !snap.StartedAt.IsZero() {
		resp.StartedAt = snap.StartedAt.UTC().Format(timeFormat)
	}
	if !snap.FinishedAt.IsZero() {
		resp.FinishedAt = snap.FinishedAt.UTC().Format(timeFormat)
	}
	writeJSON(w, http.StatusOK, resp)
}

const timeFormat = "2006-01-02T15:04:05.000Z"

type jobLogsResponse struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

// handleGetJobLogs implements GET /v1/jobs/{job_id}/logs.
func (s *Server) handleGetJobLogs(w http.ResponseWriter, r *http.Request) {
	snap, ok := s.ownedJob(w, r)
	if !ok {
		return
	}
	if s.jobs.Restored(snap.ID) {
		// The status survived a restart, the output did not: it was never
		// written to disk. 410 says the logs existed and are gone, which a
		// 404 ("no such job") would not.
		writeError(w, http.StatusGone, "output is not retained across an agent restart")
		return
	}
	stdout, stderr, stdoutTrunc, stderrTrunc, ok := s.jobs.Logs(snap.ID)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	writeJSON(w, http.StatusOK, jobLogsResponse{
		Stdout:          string(stdout),
		Stderr:          string(stderr),
		StdoutTruncated: stdoutTrunc,
		StderrTruncated: stderrTrunc,
	})
}

type healthResponse struct {
	Status  string `json:"status"`
	Agent   string `json:"agent"`
	Version string `json:"version"`
}

// handleHealth implements GET /health.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status:  "ok",
		Agent:   s.cfg.Load().AgentID,
		Version: Version,
	})
}
