// Package webhook provides an HTTP delivery handler for personal scheduled-run
// notifications.
//
// The handler is intentionally a leaf adapter: stack/personal owns durable
// notification claim, ack, retry, dead-letter, and recovery state, while this
// package only turns one claimed notification into a signed HTTP POST. Hosts
// should use endpoint-specific signing secrets and idempotency storage on the
// receiver side. Payloads include the scheduled prompt and terminal result or
// error, and zero time fields are emitted as Go zero timestamps, so production
// hosts should use WithPayload when they need redaction, a strict schema, or a
// thin event envelope.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
)

const (
	// EventTypeScheduledRunNotification is the webhook event type emitted by
	// PayloadFromRecord.
	EventTypeScheduledRunNotification = "personal.scheduled_run.notification"

	defaultTimeout       = 20 * time.Second
	defaultMaxErrorBytes = 4096
	defaultUserAgent     = "memax-personal-webhook/1"
)

var reservedHeaders = map[string]struct{}{
	"content-type":            {},
	"idempotency-key":         {},
	"user-agent":              {},
	"webhook-id":              {},
	"webhook-signature":       {},
	"webhook-timestamp":       {},
	"x-memax-notification-id": {},
	"x-memax-run-id":          {},
}

// Handler posts scheduled-run notification records to one webhook endpoint.
type Handler struct {
	endpoint      string
	client        *http.Client
	timeout       time.Duration
	headers       http.Header
	userAgent     string
	signingSecret []byte
	payload       func(personal.ScheduledRunNotificationRecord) (Payload, error)
	now           func() time.Time
	maxErrorBytes int64
}

var _ personal.ScheduledRunNotificationDeliveryHandler = (*Handler)(nil)

// Option configures a Handler.
type Option func(*Handler) error

// Payload is the JSON body sent for one scheduled-run notification delivery.
//
// The shape follows the common webhook envelope convention of a stable event
// type, event timestamp, and data object. Data is full-fidelity by default; use
// WithPayload when the receiver should only see identifiers or redacted text.
type Payload struct {
	Type      string           `json:"type"`
	Timestamp time.Time        `json:"timestamp"`
	Data      NotificationData `json:"data"`
}

// NotificationData is the default payload data for one notification record.
type NotificationData struct {
	NotificationID   string                                          `json:"notification_id"`
	RunID            string                                          `json:"run_id"`
	Status           personal.ScheduledRunStatus                     `json:"status"`
	TriggerName      string                                          `json:"trigger_name"`
	OccurrenceAt     time.Time                                       `json:"occurrence_at"`
	Prompt           string                                          `json:"prompt,omitempty"`
	Result           string                                          `json:"result,omitempty"`
	Error            string                                          `json:"error,omitempty"`
	CreatedAt        time.Time                                       `json:"created_at"`
	DeliveryStatus   personal.ScheduledRunNotificationDeliveryStatus `json:"delivery_status"`
	DeliveryAttempts int                                             `json:"delivery_attempts"`
	DeliverAfter     time.Time                                       `json:"deliver_after"`
}

// StatusError reports a non-success webhook response.
//
// Retryable is a conservative transport classification for host retry policy.
// It treats 408, 425, 429, 500, 502, 503, and 504 as retryable. Other
// non-2xx responses are treated as permanent endpoint responses.
type StatusError struct {
	StatusCode int
	Status     string
	Body       string
	Retryable  bool
}

// Error implements error.
func (e *StatusError) Error() string {
	if e == nil {
		return "<nil>"
	}
	body := strings.TrimSpace(e.Body)
	if body == "" {
		return fmt.Sprintf("webhook delivery failed: %s", e.Status)
	}
	return fmt.Sprintf("webhook delivery failed: %s: %s", e.Status, body)
}

// DeliveryError reports a non-HTTP-response delivery failure.
type DeliveryError struct {
	Op        string
	Err       error
	Retryable bool
}

// Error implements error.
func (e *DeliveryError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Op == "" {
		if e.Err == nil {
			return "webhook delivery failed"
		}
		return e.Err.Error()
	}
	if e.Err == nil {
		return e.Op + ": webhook delivery failed"
	}
	return e.Op + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Permanent returns err as a non-retryable delivery error. Payload builders can
// return this when retrying the same notification will not repair the problem.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &DeliveryError{Err: err, Retryable: false}
}

// Retryable returns err as a retryable delivery error. Payload builders can
// return this when a transient dependency prevented payload construction.
func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return &DeliveryError{Err: err, Retryable: true}
}

// New constructs a scheduled-run notification webhook delivery handler.
func New(endpoint string, options ...Option) (*Handler, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, fmt.Errorf("webhook endpoint is required")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse webhook endpoint: %w", err)
	}
	if !parsed.IsAbs() {
		return nil, fmt.Errorf("webhook endpoint must be absolute")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, fmt.Errorf("webhook endpoint scheme %q is unsupported", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("webhook endpoint host is required")
	}
	handler := &Handler{
		endpoint:  endpoint,
		client:    http.DefaultClient,
		timeout:   defaultTimeout,
		headers:   make(http.Header),
		userAgent: defaultUserAgent,
		payload: func(record personal.ScheduledRunNotificationRecord) (Payload, error) {
			return PayloadFromRecord(record), nil
		},
		now:           func() time.Time { return time.Now().UTC() },
		maxErrorBytes: defaultMaxErrorBytes,
	}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(handler); err != nil {
			return nil, err
		}
	}
	if handler.client == nil {
		return nil, fmt.Errorf("webhook HTTP client is nil")
	}
	if handler.timeout <= 0 {
		handler.timeout = defaultTimeout
	}
	if handler.userAgent == "" {
		handler.userAgent = defaultUserAgent
	}
	if handler.payload == nil {
		return nil, fmt.Errorf("webhook payload function is nil")
	}
	if handler.now == nil {
		handler.now = func() time.Time { return time.Now().UTC() }
	}
	if handler.maxErrorBytes <= 0 {
		handler.maxErrorBytes = defaultMaxErrorBytes
	}
	return handler, nil
}

// WithHTTPClient overrides the HTTP client used for delivery. Nil is rejected.
func WithHTTPClient(client *http.Client) Option {
	return func(handler *Handler) error {
		if client == nil {
			return fmt.Errorf("webhook HTTP client is nil")
		}
		handler.client = client
		return nil
	}
}

// WithTimeout sets a per-delivery timeout. Values <= 0 use the default.
func WithTimeout(timeout time.Duration) Option {
	return func(handler *Handler) error {
		handler.timeout = timeout
		return nil
	}
}

// WithHeader appends one custom header to delivery requests.
//
// Reserved transport headers are rejected because the handler owns them:
// Content-Type, User-Agent, Idempotency-Key, webhook-id, webhook-timestamp,
// webhook-signature, X-Memax-Notification-ID, and X-Memax-Run-ID.
func WithHeader(key, value string) Option {
	return func(handler *Handler) error {
		key = strings.TrimSpace(key)
		if key == "" {
			return fmt.Errorf("webhook header key is required")
		}
		if _, ok := reservedHeaders[strings.ToLower(key)]; ok {
			return fmt.Errorf("webhook header %q is reserved", key)
		}
		handler.headers.Add(key, value)
		return nil
	}
}

// WithUserAgent sets the User-Agent header. Empty values use the default.
func WithUserAgent(value string) Option {
	return func(handler *Handler) error {
		handler.userAgent = strings.TrimSpace(value)
		return nil
	}
}

// WithClock sets the clock used for signing timestamps. Production callers
// should usually omit it; tests can use it for deterministic signatures.
func WithClock(now func() time.Time) Option {
	return func(handler *Handler) error {
		if now == nil {
			return fmt.Errorf("webhook clock is nil")
		}
		handler.now = now
		return nil
	}
}

// WithHMACSecret enables Standard Webhooks-compatible HMAC-SHA256 signing.
//
// Signed requests include webhook-id, webhook-timestamp, and webhook-signature
// headers. The signed string is "id.timestamp.body", and the signature value
// uses the "v1,<base64>" header form.
func WithHMACSecret(secret []byte) Option {
	return func(handler *Handler) error {
		if len(secret) == 0 {
			return fmt.Errorf("webhook HMAC secret is required")
		}
		handler.signingSecret = append([]byte(nil), secret...)
		return nil
	}
}

// WithPayload replaces the default full-fidelity payload mapping. Use this to
// redact notification text, send a thin event, or add host-specific metadata.
// Plain errors from this function are treated as permanent DeliveryError
// values. Return Retryable(err) when payload construction depends on a
// transient host service.
func WithPayload(payload func(personal.ScheduledRunNotificationRecord) (Payload, error)) Option {
	return func(handler *Handler) error {
		if payload == nil {
			return fmt.Errorf("webhook payload function is nil")
		}
		handler.payload = payload
		return nil
	}
}

// DeliverScheduledRunNotification implements
// personal.ScheduledRunNotificationDeliveryHandler.
func (h *Handler) DeliverScheduledRunNotification(ctx context.Context, record personal.ScheduledRunNotificationRecord) error {
	if h == nil {
		return fmt.Errorf("webhook handler is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := h.payload(record)
	if err != nil {
		return deliveryError("build webhook payload", err, false)
	}
	if payload.Type == "" {
		payload.Type = EventTypeScheduledRunNotification
	}
	if payload.Timestamp.IsZero() {
		payload.Timestamp = payloadTimestamp(record)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return deliveryError("encode webhook payload", err, false)
	}
	reqCtx := ctx
	var cancel context.CancelFunc
	if h.timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, h.endpoint, bytes.NewReader(body))
	if err != nil {
		return deliveryError("build webhook request", err, false)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", h.userAgent)
	req.Header.Set("Idempotency-Key", record.ID)
	req.Header.Set("webhook-id", record.ID)
	req.Header.Set("X-Memax-Notification-ID", record.ID)
	req.Header.Set("X-Memax-Run-ID", record.RunID)
	for key, values := range h.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	h.sign(req, body)

	resp, err := h.client.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return deliveryError("perform webhook request", err, true)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, h.maxErrorBytes))
	if readErr != nil {
		bodyBytes = append(bodyBytes, []byte(" <read error: "+readErr.Error()+">")...)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return &StatusError{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       string(bodyBytes),
		Retryable:  statusRetryable(resp.StatusCode),
	}
}

// PayloadFromRecord returns the default webhook payload for one notification
// record.
func PayloadFromRecord(record personal.ScheduledRunNotificationRecord) Payload {
	return Payload{
		Type:      EventTypeScheduledRunNotification,
		Timestamp: payloadTimestamp(record),
		Data: NotificationData{
			NotificationID:   record.ID,
			RunID:            record.RunID,
			Status:           record.Status,
			TriggerName:      record.TriggerName,
			OccurrenceAt:     record.OccurrenceAt,
			Prompt:           record.Prompt,
			Result:           record.Result,
			Error:            record.Error,
			CreatedAt:        record.CreatedAt,
			DeliveryStatus:   record.DeliveryStatus,
			DeliveryAttempts: record.DeliveryAttempts,
			DeliverAfter:     record.DeliverAfter,
		},
	}
}

func (h *Handler) sign(req *http.Request, body []byte) {
	if len(h.signingSecret) == 0 {
		return
	}
	timestamp := strconv.FormatInt(h.now().Unix(), 10)
	req.Header.Set("webhook-timestamp", timestamp)
	message := req.Header.Get("webhook-id") + "." + timestamp + "." + string(body)
	mac := hmac.New(sha256.New, h.signingSecret)
	_, _ = mac.Write([]byte(message))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	req.Header.Set("webhook-signature", "v1,"+signature)
}

func payloadTimestamp(record personal.ScheduledRunNotificationRecord) time.Time {
	for _, candidate := range []time.Time{
		record.CreatedAt,
		record.DeliveryUpdatedAt,
		record.DeliverAfter,
		record.OccurrenceAt,
	} {
		if !candidate.IsZero() {
			return candidate.UTC()
		}
	}
	return time.Time{}
}

func statusRetryable(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout,
		http.StatusTooEarly,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func deliveryError(op string, err error, defaultRetryable bool) error {
	var existing *DeliveryError
	if errors.As(err, &existing) {
		return &DeliveryError{Op: op, Err: err, Retryable: existing.Retryable}
	}
	return &DeliveryError{Op: op, Err: err, Retryable: defaultRetryable}
}
