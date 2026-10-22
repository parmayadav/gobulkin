package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/parmayadav/gobulkin/pkg/loader"
)

const (
	defaultMaxImports = 4
	retryAfterSeconds = "5"
)

// txStarter is satisfied by *pgx.Conn and *pgxpool.Pool.
type txStarter interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type routerConfig struct {
	loader               *loader.Loader
	db                   txStarter
	apiKey               string
	logger               *slog.Logger
	maxConcurrentImports int
}

func newRouter(cfg routerConfig) http.Handler {
	h := &uploadHandler{loader: cfg.loader, db: cfg.db, logger: cfg.logger}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Auth runs first so unauthenticated callers never hold an import slot.
	importRoute := requireAPIKey(cfg.apiKey,
		limitConcurrency(cfg.maxConcurrentImports, http.HandlerFunc(h.importCustomers)))
	mux.Handle("POST /v1/customers/import", importRoute)
	return mux
}

type uploadHandler struct {
	loader *loader.Loader
	db     txStarter
	logger *slog.Logger
}

// importResponse is the report plus whether it was committed.
type importResponse struct {
	*loader.Report
	Committed bool `json:"committed"`
}

// importCustomers owns the transaction and commits or aborts based on the loader's report.
func (h *uploadHandler) importCustomers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	params, err := parseImportParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	file, err := firstFile(r)
	if err != nil {
		status, message := statusFor(err)
		writeError(w, status, message)
		return
	}

	tx, err := h.db.Begin(ctx)
	if err != nil {
		h.logger.Error("begin failed", "error", err)
		writeError(w, http.StatusInternalServerError, "import failed")
		return
	}
	// No-op after a successful Commit; the fresh context survives a cancelled request.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	report, err := h.loader.ImportFile(ctx, tx, file.FileName(), file, params.options)
	if err != nil {
		status, message := statusFor(err)
		if status >= http.StatusInternalServerError {
			h.logger.Error("import failed", "error", err)
		}
		writeError(w, status, message)
		return
	}

	if report.HasRejectedRows() && !params.commitPartial {
		// The deferred rollback discards the valid rows too.
		writeJSON(w, http.StatusUnprocessableEntity, importResponse{Report: report})
		return
	}

	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("commit failed", "error", err)
		writeError(w, http.StatusInternalServerError, "import failed")
		return
	}
	writeJSON(w, http.StatusOK, importResponse{Report: report, Committed: true})
}

// requireAPIKey stands in for real authentication.
func requireAPIKey(apiKey string, next http.Handler) http.Handler {
	// Fixed-length digests make the comparison constant-time.
	want := sha256.Sum256([]byte(apiKey))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		got := sha256.Sum256([]byte(token))
		if !hasBearer || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// limitConcurrency bounds how many imports run at once.
func limitConcurrency(limit int, next http.Handler) http.Handler {
	slots := make(chan struct{}, limit)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", retryAfterSeconds)
			writeError(w, http.StatusTooManyRequests, "too many imports in progress")
		}
	})
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
