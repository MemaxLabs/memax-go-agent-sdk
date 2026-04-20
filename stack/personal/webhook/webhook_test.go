package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/stack/personal"
)

func TestHandlerDeliversSignedWebhook(t *testing.T) {
	record := testNotificationRecord()
	attemptAt := time.Date(2026, 4, 20, 10, 15, 0, 0, time.UTC)
	secret := []byte("endpoint-signing-secret")
	var got struct {
		method           string
		contentType      string
		userAgent        string
		idempotencyKey   string
		webhookID        string
		webhookTimestamp string
		webhookSignature string
		customHeader     string
		payload          Payload
		body             string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		got.method = r.Method
		got.contentType = r.Header.Get("Content-Type")
		got.userAgent = r.Header.Get("User-Agent")
		got.idempotencyKey = r.Header.Get("Idempotency-Key")
		got.webhookID = r.Header.Get("webhook-id")
		got.webhookTimestamp = r.Header.Get("webhook-timestamp")
		got.webhookSignature = r.Header.Get("webhook-signature")
		got.customHeader = r.Header.Get("X-Host-Route")
		got.body = string(body)
		if err := json.Unmarshal(body, &got.payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	handler, err := New(
		server.URL,
		WithHTTPClient(server.Client()),
		WithHMACSecret(secret),
		WithHeader("X-Host-Route", "personal-alerts"),
		WithUserAgent("host-notifier/1"),
		WithClock(func() time.Time { return attemptAt }),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := handler.DeliverScheduledRunNotification(context.Background(), record); err != nil {
		t.Fatalf("DeliverScheduledRunNotification() error = %v", err)
	}

	if got.method != http.MethodPost {
		t.Fatalf("method = %q, want POST", got.method)
	}
	if got.contentType != "application/json" || got.userAgent != "host-notifier/1" {
		t.Fatalf("headers content-type=%q user-agent=%q, want JSON and custom UA", got.contentType, got.userAgent)
	}
	if got.idempotencyKey != record.ID || got.webhookID != record.ID {
		t.Fatalf("idempotency headers = %q/%q, want %q", got.idempotencyKey, got.webhookID, record.ID)
	}
	if got.webhookTimestamp != "1776680100" {
		t.Fatalf("webhook timestamp = %q, want attempt unix timestamp", got.webhookTimestamp)
	}
	if got.customHeader != "personal-alerts" {
		t.Fatalf("custom header = %q, want personal-alerts", got.customHeader)
	}
	wantSignature := standardSignature(secret, record.ID, got.webhookTimestamp, got.body)
	if got.webhookSignature != wantSignature {
		t.Fatalf("webhook signature = %q, want %q", got.webhookSignature, wantSignature)
	}
	if got.payload.Type != EventTypeScheduledRunNotification || !got.payload.Timestamp.Equal(record.CreatedAt) {
		t.Fatalf("payload envelope = %#v, want default event and created timestamp", got.payload)
	}
	if got.payload.Data.NotificationID != record.ID || got.payload.Data.RunID != record.RunID || got.payload.Data.Result != record.Result {
		t.Fatalf("payload data = %#v, want record fields", got.payload.Data)
	}
}

func TestHandlerUsesCustomPayload(t *testing.T) {
	record := testNotificationRecord()
	var payload Payload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	handler, err := New(
		server.URL,
		WithHTTPClient(server.Client()),
		WithPayload(func(record personal.ScheduledRunNotificationRecord) (Payload, error) {
			return Payload{
				Type:      "personal.scheduled_run.thin",
				Timestamp: record.CreatedAt,
				Data: NotificationData{
					NotificationID: record.ID,
					RunID:          record.RunID,
					Status:         record.Status,
				},
			}, nil
		}),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := handler.DeliverScheduledRunNotification(context.Background(), record); err != nil {
		t.Fatalf("DeliverScheduledRunNotification() error = %v", err)
	}
	if payload.Type != "personal.scheduled_run.thin" || payload.Data.Prompt != "" || payload.Data.Result != "" {
		t.Fatalf("payload = %#v, want thin redacted event", payload)
	}
}

func TestHandlerReportsRetryableStatusError(t *testing.T) {
	record := testNotificationRecord()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, strings.Repeat("x", 5000), http.StatusServiceUnavailable)
	}))
	defer server.Close()
	handler, err := New(server.URL, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = handler.DeliverScheduledRunNotification(context.Background(), record)
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("DeliverScheduledRunNotification() error = %T %[1]v, want StatusError", err)
	}
	if statusErr.StatusCode != http.StatusServiceUnavailable || !statusErr.Retryable {
		t.Fatalf("StatusError = %#v, want retryable 503", statusErr)
	}
	if len(statusErr.Body) != defaultMaxErrorBytes {
		t.Fatalf("StatusError body length = %d, want capped %d", len(statusErr.Body), defaultMaxErrorBytes)
	}
}

func TestHandlerReportsPermanentStatusError(t *testing.T) {
	record := testNotificationRecord()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad recipient", http.StatusBadRequest)
	}))
	defer server.Close()
	handler, err := New(server.URL, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = handler.DeliverScheduledRunNotification(context.Background(), record)
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("DeliverScheduledRunNotification() error = %T %[1]v, want StatusError", err)
	}
	if statusErr.StatusCode != http.StatusBadRequest || statusErr.Retryable {
		t.Fatalf("StatusError = %#v, want permanent 400", statusErr)
	}
}

func TestHandlerReportsPermanentPayloadError(t *testing.T) {
	payloadErr := errors.New("redaction policy missing")
	handler, err := New(
		"https://example.com/hook",
		WithPayload(func(personal.ScheduledRunNotificationRecord) (Payload, error) {
			return Payload{}, payloadErr
		}),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = handler.DeliverScheduledRunNotification(context.Background(), testNotificationRecord())
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("DeliverScheduledRunNotification() error = %T %[1]v, want DeliveryError", err)
	}
	if deliveryErr.Retryable || !errors.Is(err, payloadErr) {
		t.Fatalf("DeliveryError = %#v, want permanent wrapper around payload error", deliveryErr)
	}
}

func TestHandlerPreservesRetryablePayloadError(t *testing.T) {
	payloadErr := errors.New("redaction service unavailable")
	handler, err := New(
		"https://example.com/hook",
		WithPayload(func(personal.ScheduledRunNotificationRecord) (Payload, error) {
			return Payload{}, Retryable(payloadErr)
		}),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = handler.DeliverScheduledRunNotification(context.Background(), testNotificationRecord())
	var deliveryErr *DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("DeliverScheduledRunNotification() error = %T %[1]v, want DeliveryError", err)
	}
	if !deliveryErr.Retryable || !errors.Is(err, payloadErr) {
		t.Fatalf("DeliveryError = %#v, want retryable wrapper around payload error", deliveryErr)
	}
}

func TestHandlerPropagatesCanceledContext(t *testing.T) {
	handler, err := New("https://example.com/hook")
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = handler.DeliverScheduledRunNotification(ctx, testNotificationRecord())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DeliverScheduledRunNotification(canceled) error = %v, want context.Canceled", err)
	}
}

func TestStatusRetryableClassification(t *testing.T) {
	for _, code := range []int{
		http.StatusRequestTimeout,
		http.StatusTooEarly,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		if !statusRetryable(code) {
			t.Fatalf("statusRetryable(%d) = false, want true", code)
		}
	}
	for _, code := range []int{
		http.StatusBadRequest,
		http.StatusNotFound,
		http.StatusNotImplemented,
		http.StatusHTTPVersionNotSupported,
	} {
		if statusRetryable(code) {
			t.Fatalf("statusRetryable(%d) = true, want false", code)
		}
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("New(empty) error = nil, want error")
	}
	if _, err := New("://bad"); err == nil {
		t.Fatal("New(bad URL) error = nil, want error")
	}
	if _, err := New("ftp://example.com/hook"); err == nil {
		t.Fatal("New(ftp) error = nil, want error")
	}
	if _, err := New("https:///hook"); err == nil {
		t.Fatal("New(no host) error = nil, want error")
	}
	if _, err := New("https://example.com/hook", WithHeader("webhook-id", "bad")); err == nil {
		t.Fatal("New(reserved header) error = nil, want error")
	}
	if _, err := New("https://example.com/hook", WithHTTPClient(nil)); err == nil {
		t.Fatal("New(nil client) error = nil, want error")
	}
	if _, err := New("https://example.com/hook", WithHeader("", "value")); err == nil {
		t.Fatal("New(empty header) error = nil, want error")
	}
	if _, err := New("https://example.com/hook", WithHMACSecret(nil)); err == nil {
		t.Fatal("New(empty HMAC secret) error = nil, want error")
	}
	if _, err := New("https://example.com/hook", WithPayload(nil)); err == nil {
		t.Fatal("New(nil payload) error = nil, want error")
	}
	if _, err := New("https://example.com/hook", WithClock(nil)); err == nil {
		t.Fatal("New(nil clock) error = nil, want error")
	}
}

func standardSignature(secret []byte, id, timestamp, body string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(id + "." + timestamp + "." + body))
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func testNotificationRecord() personal.ScheduledRunNotificationRecord {
	createdAt := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	return personal.ScheduledRunNotificationRecord{
		ID:               "scheduled-run-1:succeeded",
		RunID:            "scheduled-run-1",
		Status:           personal.ScheduledRunSucceeded,
		TriggerName:      "daily-brief",
		OccurrenceAt:     time.Date(2026, 4, 20, 7, 0, 0, 0, time.UTC),
		Prompt:           "Build my daily brief.",
		Result:           "Daily brief is ready.",
		CreatedAt:        createdAt,
		DeliveryStatus:   personal.ScheduledRunNotificationDeliveryDelivering,
		DeliveryWorkerID: "worker-1",
		DeliveryAttempts: 2,
		DeliverAfter:     createdAt,
	}
}
