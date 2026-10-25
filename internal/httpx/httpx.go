// Package httpx holds the HTTP helpers shared by the player API and the admin API.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/go-playground/validator/v10"

	"brunoplay/internal/apperr"
)

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyPlayer
	keyAdmin
	keyLang
	keyIP
)

var validate = validator.New(validator.WithRequiredStructEnabled())

// Validator exposes the shared validator for custom registrations.
func Validator() *validator.Validate { return validate }

func WithValue(ctx context.Context, k ctxKey, v any) context.Context {
	return context.WithValue(ctx, k, v)
}

// RequestID returns the id of the current request ("" outside a request).
func RequestID(ctx context.Context) string {
	s, _ := ctx.Value(keyRequestID).(string)
	return s
}

func IP(ctx context.Context) string {
	s, _ := ctx.Value(keyIP).(string)
	return s
}

// Lang is the language for error texts: the player's, else Accept-Language, else English.
func Lang(ctx context.Context) apperr.Lang {
	if l, ok := ctx.Value(keyLang).(apperr.Lang); ok {
		return l
	}
	return apperr.EN
}

type errBody struct {
	Error struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		Params    map[string]any `json:"params,omitempty"`
		RequestID string         `json:"requestId,omitempty"`
	} `json:"error"`
}

// JSON writes a JSON response.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// Fail renders any error as the standard error body. Unknown errors become INTERNAL and are logged.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	ae := apperr.As(err)
	if ae == nil {
		slog.Error("request failed", "err", err, "path", r.URL.Path, "method", r.Method, "request_id", RequestID(r.Context()))
		ae = apperr.Internal
	} else if ae.Status >= 500 && ae.Code == apperr.Internal.Code {
		slog.Error("request failed", "err", ae, "path", r.URL.Path, "request_id", RequestID(r.Context()))
	}
	var b errBody
	b.Error.Code = ae.Code
	b.Error.Message = ae.Message(Lang(r.Context()))
	b.Error.Params = ae.Params
	b.Error.RequestID = RequestID(r.Context())
	JSON(w, ae.Status, b)
}

// Decode reads a JSON body strictly (unknown fields refused, 1 MB limit) and validates it.
func Decode(r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return apperr.BadRequest
		}
		return apperr.BadRequest.Wrap(err)
	}
	if err := validate.Struct(dst); err != nil {
		var ve validator.ValidationErrors
		if errors.As(err, &ve) && len(ve) > 0 {
			return apperr.Validation.With("field", ve[0].Field()).Wrap(err)
		}
		return apperr.Validation.Wrap(err)
	}
	return nil
}

// DecodeOptional is Decode but an empty body is fine.
func DecodeOptional(r *http.Request, dst any) error {
	if r.ContentLength == 0 {
		return nil
	}
	if err := Decode(r, dst); err != nil {
		if apperr.Is(err, apperr.BadRequest) && errors.Is(errors.Unwrap(err), io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

// ClientIP returns the client address, trusting X-Forwarded-For only behind our own proxy.
func ClientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
			parts := strings.Split(xf, ",")
			if ip := strings.TrimSpace(parts[0]); ip != "" {
				return ip
			}
		}
		if xr := r.Header.Get("X-Real-IP"); xr != "" {
			return strings.TrimSpace(xr)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Handler is a handler that returns an error (rendered by Wrap).
type Handler func(w http.ResponseWriter, r *http.Request) error

func Wrap(h Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			Fail(w, r, err)
		}
	}
}
