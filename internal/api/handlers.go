package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"

	"axiom/internal/audit"
	"axiom/internal/jobs"
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

	allowed := s.cfg.Identities[identity.CommonName]
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
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
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

	job, err := s.jobs.Trigger(r.Context(), action, identity.CommonName, req.Parameters)
	if err != nil {
		switch {
		case errors.Is(err, jobs.ErrUnknownAction):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, jobs.ErrUnknownParameter), errors.Is(err, jobs.ErrInvalidParameter), errors.Is(err, jobs.ErrMissingParameter):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, jobs.ErrActionBusy):
			writeError(w, http.StatusConflict, err.Error())
		default:
			s.logger.Error("triggering action failed", "action", action, "error", err)
			writeError(w, http.StatusInternalServerError, "failed to start action")
		}
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"job_id": job.ID,
		"status": string(jobs.StatusQueued),
	})
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
	if identityFromContext(r.Context()) == nil {
		s.rejectUnauthenticated(w, r, "missing identity")
		return
	}

	jobID := r.PathValue("job_id")
	if !jobs.ValidID(jobID) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	snap, ok := s.jobs.Get(jobID)
	if !ok {
		writeError(w, http.StatusNotFound, "job not found")
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
	if identityFromContext(r.Context()) == nil {
		s.rejectUnauthenticated(w, r, "missing identity")
		return
	}

	jobID := r.PathValue("job_id")
	if !jobs.ValidID(jobID) {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}
	stdout, stderr, stdoutTrunc, stderrTrunc, ok := s.jobs.Logs(jobID)
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
		Agent:   s.cfg.AgentID,
		Version: Version,
	})
}
