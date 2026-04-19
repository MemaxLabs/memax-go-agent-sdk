// Package jmapclient provides a focused JMAP mail client for messaging
// adapters.
//
// QueryEmails currently returns a single page bounded by QueryRequest.Limit.
// Large inbox workflows should keep query text narrow until bounded paging
// lands as a follow-up.
package jmapclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultTimeout        = 30 * time.Second
	defaultMaxBytes int64 = 4 * 1024 * 1024
)

const (
	coreCapability = "urn:ietf:params:jmap:core"
	mailCapability = "urn:ietf:params:jmap:mail"
)

var (
	// ErrNotFound reports a missing JMAP object.
	ErrNotFound = errors.New("jmap object not found")
)

// Option mutates one client configuration field.
type Option func(*Client)

// Client is a focused JMAP client over one account.
type Client struct {
	baseURL    *url.URL
	accountID  string
	httpClient *http.Client
	timeout    time.Duration
	maxBytes   int64
	headers    http.Header
	token      string
}

// QueryRequest scopes one Email/query request.
type QueryRequest struct {
	Text            string
	Limit           int
	CollapseThreads bool
}

// EmailGetRequest scopes one Email/get request.
type EmailGetRequest struct {
	IDs                 []string
	Properties          []string
	FetchTextBodyValues bool
	FetchHTMLBodyValues bool
	MaxBodyValueBytes   int
}

// EmailAddress is one JMAP address object.
type EmailAddress struct {
	Name  string
	Email string
}

// BodyPart is one body-part reference from Email textBody/htmlBody lists.
type BodyPart struct {
	PartID string
}

// BodyValue is one decoded text body value.
type BodyValue struct {
	Value       string
	IsTruncated bool
}

// Email is the JMAP email representation used by messaging adapters.
type Email struct {
	ID         string
	ThreadID   string
	MailboxIDs map[string]bool
	Keywords   map[string]bool
	Subject    string
	Preview    string
	ReceivedAt time.Time
	From       []EmailAddress
	To         []EmailAddress
	CC         []EmailAddress
	BCC        []EmailAddress
	TextBody   []BodyPart
	HTMLBody   []BodyPart
	BodyValues map[string]BodyValue
}

// Thread is the JMAP thread representation used by messaging adapters.
type Thread struct {
	ID       string
	EmailIDs []string
}

// New returns a JMAP client for one API URL and account.
func New(baseURL, accountID string, opts ...Option) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("jmap base url is required")
	}
	accountID = strings.TrimSpace(accountID)
	if accountID == "" {
		return nil, fmt.Errorf("jmap account id is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse jmap base url: %w", err)
	}
	if !parsed.IsAbs() {
		return nil, fmt.Errorf("jmap base url must be absolute")
	}
	client := &Client{
		baseURL:    parsed,
		accountID:  accountID,
		httpClient: http.DefaultClient,
		timeout:    defaultTimeout,
		maxBytes:   defaultMaxBytes,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(client)
		}
	}
	if client.httpClient == nil {
		client.httpClient = http.DefaultClient
	}
	if client.timeout <= 0 {
		client.timeout = defaultTimeout
	}
	if client.maxBytes <= 0 {
		client.maxBytes = defaultMaxBytes
	}
	return client, nil
}

// WithHTTPClient sets the HTTP client used for requests.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		c.httpClient = client
	}
}

// WithTimeout sets a per-request timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) {
		c.timeout = timeout
	}
}

// WithMaxBytes sets the maximum response body size.
func WithMaxBytes(maxBytes int64) Option {
	return func(c *Client) {
		c.maxBytes = maxBytes
	}
}

// WithHeaders clones and attaches extra headers to each request.
func WithHeaders(headers http.Header) Option {
	return func(c *Client) {
		c.headers = cloneHeader(headers)
	}
}

// WithBearerToken configures a bearer token for requests.
func WithBearerToken(token string) Option {
	return func(c *Client) {
		c.token = strings.TrimSpace(token)
	}
}

// QueryEmails returns matching email IDs.
func (c *Client) QueryEmails(ctx context.Context, req QueryRequest) ([]string, error) {
	if c == nil {
		return nil, fmt.Errorf("nil jmap client")
	}
	args := map[string]any{
		"accountId":       c.accountID,
		"collapseThreads": req.CollapseThreads,
		"sort": []map[string]any{{
			"property":    "receivedAt",
			"isAscending": false,
		}},
	}
	if text := strings.TrimSpace(req.Text); text != "" {
		args["filter"] = map[string]any{"text": text}
	}
	if req.Limit > 0 {
		args["limit"] = req.Limit
	}
	var response queryResponse
	if err := c.call(ctx, "Email/query", args, &response); err != nil {
		return nil, err
	}
	return append([]string(nil), response.IDs...), nil
}

// GetEmails loads one or more emails by ID.
func (c *Client) GetEmails(ctx context.Context, req EmailGetRequest) ([]Email, error) {
	if c == nil {
		return nil, fmt.Errorf("nil jmap client")
	}
	ids := compactStrings(req.IDs)
	if len(ids) == 0 {
		return nil, fmt.Errorf("jmap email ids are required")
	}
	args := map[string]any{
		"accountId":  c.accountID,
		"ids":        ids,
		"properties": append([]string(nil), req.Properties...),
	}
	if req.FetchTextBodyValues {
		args["fetchTextBodyValues"] = true
	}
	if req.FetchHTMLBodyValues {
		args["fetchHTMLBodyValues"] = true
	}
	if req.MaxBodyValueBytes > 0 {
		args["maxBodyValueBytes"] = req.MaxBodyValueBytes
	}
	var response emailGetResponse
	if err := c.call(ctx, "Email/get", args, &response); err != nil {
		return nil, err
	}
	emails := make([]Email, 0, len(response.List))
	for _, item := range response.List {
		emails = append(emails, item.email())
	}
	return emails, nil
}

// GetThreads loads one or more threads by ID.
func (c *Client) GetThreads(ctx context.Context, ids []string) ([]Thread, error) {
	if c == nil {
		return nil, fmt.Errorf("nil jmap client")
	}
	ids = compactStrings(ids)
	if len(ids) == 0 {
		return nil, fmt.Errorf("jmap thread ids are required")
	}
	var response threadGetResponse
	if err := c.call(ctx, "Thread/get", map[string]any{
		"accountId": c.accountID,
		"ids":       ids,
	}, &response); err != nil {
		return nil, err
	}
	threads := make([]Thread, 0, len(response.List))
	for _, item := range response.List {
		threads = append(threads, Thread{
			ID:       strings.TrimSpace(item.ID),
			EmailIDs: compactStrings(item.EmailIDs),
		})
	}
	return threads, nil
}

func (c *Client) call(ctx context.Context, method string, args any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	payload, err := json.Marshal(requestEnvelope{
		Using: []string{coreCapability, mailCapability},
		MethodCalls: []methodCall{{
			Name:   method,
			Args:   args,
			CallID: "0",
		}},
	})
	if err != nil {
		return fmt.Errorf("encode jmap %s request: %w", method, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL.String(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build jmap %s request: %w", method, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(c.token); token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	mergeHeader(httpReq.Header, c.headers)

	response, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("jmap %s request failed: %w", method, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("jmap %s %s returned status %d", httpReq.Method, httpReq.URL.String(), response.StatusCode)
	}
	body, err := readBodyLimited(response.Body, c.maxBytes)
	if err != nil {
		return fmt.Errorf("read jmap %s response: %w", method, err)
	}
	var envelope responseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("decode jmap %s response: %w", method, err)
	}
	if len(envelope.MethodResponses) == 0 {
		return fmt.Errorf("decode jmap %s response: empty methodResponses", method)
	}
	item := envelope.MethodResponses[0]
	if item.Name == "error" {
		var methodErr methodError
		if err := json.Unmarshal(item.Args, &methodErr); err != nil {
			return fmt.Errorf("decode jmap %s error: %w", method, err)
		}
		if strings.EqualFold(methodErr.Type, "notFound") {
			return ErrNotFound
		}
		return fmt.Errorf("jmap %s error: %s", method, firstNonEmpty(methodErr.Description, methodErr.Type))
	}
	if item.Name != method {
		return fmt.Errorf("decode jmap %s response: unexpected method %s", method, item.Name)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(item.Args, out); err != nil {
		return fmt.Errorf("decode jmap %s payload: %w", method, err)
	}
	return nil
}

type requestEnvelope struct {
	Using       []string     `json:"using"`
	MethodCalls []methodCall `json:"methodCalls"`
}

type methodCall struct {
	Name   string
	Args   any
	CallID string
}

func (m methodCall) MarshalJSON() ([]byte, error) {
	args, err := json.Marshal(m.Args)
	if err != nil {
		return nil, err
	}
	return json.Marshal([]any{m.Name, json.RawMessage(args), m.CallID})
}

type responseEnvelope struct {
	MethodResponses []methodResponse `json:"methodResponses"`
}

type methodResponse struct {
	Name   string
	Args   json.RawMessage
	CallID string
}

func (m *methodResponse) UnmarshalJSON(data []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	if len(parts) != 3 {
		return fmt.Errorf("invalid jmap method response length %d", len(parts))
	}
	if err := json.Unmarshal(parts[0], &m.Name); err != nil {
		return err
	}
	m.Args = append(m.Args[:0], parts[1]...)
	return json.Unmarshal(parts[2], &m.CallID)
}

type methodError struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

type queryResponse struct {
	IDs []string `json:"ids"`
}

type emailGetResponse struct {
	List []rawEmail `json:"list"`
}

type threadGetResponse struct {
	List []rawThread `json:"list"`
}

type rawThread struct {
	ID       string   `json:"id"`
	EmailIDs []string `json:"emailIds"`
}

type rawEmail struct {
	ID         string            `json:"id"`
	ThreadID   string            `json:"threadId"`
	MailboxIDs map[string]bool   `json:"mailboxIds"`
	Keywords   map[string]bool   `json:"keywords"`
	Subject    string            `json:"subject"`
	Preview    string            `json:"preview"`
	ReceivedAt string            `json:"receivedAt"`
	From       []rawEmailAddress `json:"from"`
	To         []rawEmailAddress `json:"to"`
	CC         []rawEmailAddress `json:"cc"`
	BCC        []rawEmailAddress `json:"bcc"`
	TextBody   []rawBodyPart     `json:"textBody"`
	HTMLBody   []rawBodyPart     `json:"htmlBody"`
	BodyValues map[string]struct {
		Value       string `json:"value"`
		IsTruncated bool   `json:"isTruncated"`
	} `json:"bodyValues"`
}

type rawEmailAddress struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type rawBodyPart struct {
	PartID string `json:"partId"`
}

func (r rawEmail) email() Email {
	out := Email{
		ID:         strings.TrimSpace(r.ID),
		ThreadID:   strings.TrimSpace(r.ThreadID),
		MailboxIDs: cloneBoolMap(r.MailboxIDs),
		Keywords:   cloneBoolMap(r.Keywords),
		Subject:    strings.TrimSpace(r.Subject),
		Preview:    strings.TrimSpace(r.Preview),
		ReceivedAt: parseTime(r.ReceivedAt),
		From:       addresses(r.From),
		To:         addresses(r.To),
		CC:         addresses(r.CC),
		BCC:        addresses(r.BCC),
		TextBody:   bodyParts(r.TextBody),
		HTMLBody:   bodyParts(r.HTMLBody),
		BodyValues: make(map[string]BodyValue, len(r.BodyValues)),
	}
	for partID, value := range r.BodyValues {
		out.BodyValues[strings.TrimSpace(partID)] = BodyValue{
			Value:       value.Value,
			IsTruncated: value.IsTruncated,
		}
	}
	return out
}

func bodyParts(items []rawBodyPart) []BodyPart {
	if len(items) == 0 {
		return nil
	}
	out := make([]BodyPart, 0, len(items))
	for _, item := range items {
		partID := strings.TrimSpace(item.PartID)
		if partID == "" {
			continue
		}
		out = append(out, BodyPart{PartID: partID})
	}
	return out
}

func addresses(items []rawEmailAddress) []EmailAddress {
	if len(items) == 0 {
		return nil
	}
	out := make([]EmailAddress, 0, len(items))
	for _, item := range items {
		address := strings.TrimSpace(item.Email)
		name := strings.TrimSpace(item.Name)
		if address == "" && name == "" {
			continue
		}
		out = append(out, EmailAddress{Name: name, Email: address})
	}
	return out
}

func cloneHeader(header http.Header) http.Header {
	if len(header) == 0 {
		return nil
	}
	out := make(http.Header, len(header))
	for key, values := range header {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func mergeHeader(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func cloneBoolMap(src map[string]bool) map[string]bool {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]bool, len(src))
	for key, value := range src {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func parseTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func readBodyLimited(body io.Reader, maxBytes int64) ([]byte, error) {
	limited := io.LimitReader(body, maxBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("response body exceeded %d bytes", maxBytes)
	}
	return payload, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
