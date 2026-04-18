// Package googlecalendarclient provides a focused Google Calendar REST client
// for scheduling adapters.
package googlecalendarclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
)

const (
	defaultBaseURL  = "https://www.googleapis.com/calendar/v3"
	defaultTimeout  = 30 * time.Second
	defaultMaxBytes = 4 * 1024 * 1024
	defaultMaxPages = 5
)

var (
	// ErrNotFound reports a missing Google Calendar event.
	ErrNotFound = errors.New("google calendar event not found")
	// ErrConflict reports a precondition failure during mutation.
	ErrConflict = errors.New("google calendar conflict")
)

// Option mutates one client configuration field.
type Option func(*Client)

// Client is a focused Google Calendar client over one calendar ID.
type Client struct {
	baseURL    *url.URL
	calendarID string
	httpClient *http.Client
	timeout    time.Duration
	maxBytes   int64
	maxPages   int
	headers    http.Header
	token      string
}

// ListRequest scopes one events.list call.
type ListRequest struct {
	Q          string
	ICalUID    string
	TimeMin    time.Time
	TimeMax    time.Time
	MaxResults int
}

// Event is the Google Calendar event representation used by the adapter.
type Event struct {
	ID                        string
	ICalUID                   string
	ETag                      string
	Status                    string
	Summary                   string
	Description               string
	Location                  string
	Organizer                 scheduling.Participant
	Attendees                 []scheduling.Participant
	Start                     time.Time
	End                       time.Time
	TimeZone                  string
	Created                   time.Time
	Updated                   time.Time
	ExtendedPropertiesPrivate map[string]string
}

// New returns a Google Calendar client for one calendar.
func New(calendarID string, opts ...Option) (*Client, error) {
	calendarID = strings.TrimSpace(calendarID)
	if calendarID == "" {
		return nil, fmt.Errorf("google calendar id is required")
	}
	baseURL, err := url.Parse(defaultBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse google calendar base url: %w", err)
	}
	client := &Client{
		baseURL:    baseURL,
		calendarID: calendarID,
		httpClient: http.DefaultClient,
		timeout:    defaultTimeout,
		maxBytes:   defaultMaxBytes,
		maxPages:   defaultMaxPages,
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
	if client.maxPages <= 0 {
		client.maxPages = defaultMaxPages
	}
	return client, nil
}

// WithBaseURL overrides the Google Calendar API base URL.
func WithBaseURL(raw string) Option {
	return func(c *Client) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		if parsed, err := url.Parse(raw); err == nil && parsed.IsAbs() {
			c.baseURL = parsed
		}
	}
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

// WithMaxPages sets the maximum number of events.list pages fetched per call.
func WithMaxPages(maxPages int) Option {
	return func(c *Client) {
		c.maxPages = maxPages
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

// List returns matching events from one calendar.
func (c *Client) List(ctx context.Context, req ListRequest) ([]Event, error) {
	if c == nil {
		return nil, fmt.Errorf("nil google calendar client")
	}
	var (
		items     []Event
		pageToken string
	)
	for page := 0; page < c.maxPages; page++ {
		u := c.eventsURL()
		query := u.Query()
		query.Set("singleEvents", "true")
		query.Set("showDeleted", "false")
		if text := strings.TrimSpace(req.Q); text != "" {
			query.Set("q", text)
		}
		if uid := strings.TrimSpace(req.ICalUID); uid != "" {
			query.Set("iCalUID", uid)
		}
		if !req.TimeMin.IsZero() {
			query.Set("timeMin", req.TimeMin.UTC().Format(time.RFC3339))
		}
		if !req.TimeMax.IsZero() {
			query.Set("timeMax", req.TimeMax.UTC().Format(time.RFC3339))
		}
		if req.MaxResults > 0 {
			query.Set("maxResults", strconv.Itoa(req.MaxResults))
		}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		if strings.TrimSpace(req.ICalUID) == "" {
			query.Set("orderBy", "startTime")
		}
		query.Set("fields", "items(id,iCalUID,etag,status,summary,description,location,organizer,attendees,start,end,created,updated,extendedProperties/private),nextPageToken")
		u.RawQuery = query.Encode()

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, fmt.Errorf("build google calendar list request: %w", err)
		}
		response, err := c.do(httpReq)
		if err != nil {
			return nil, err
		}
		payload, readErr := readBodyLimited(response.Body, c.maxBytes)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, c.responseError(httpReq.Method, httpReq.URL.String(), response.StatusCode)
		}
		if readErr != nil {
			return nil, fmt.Errorf("read google calendar list response: %w", readErr)
		}
		var envelope eventsListResponse
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return nil, fmt.Errorf("decode google calendar list response: %w", err)
		}
		for _, item := range envelope.Items {
			event, err := item.event()
			if err != nil {
				return nil, fmt.Errorf("decode google calendar event %s: %w", item.ID, err)
			}
			items = append(items, event)
		}
		pageToken = strings.TrimSpace(envelope.NextPageToken)
		if pageToken == "" {
			break
		}
	}
	return items, nil
}

// Get loads one full event by event ID.
func (c *Client) Get(ctx context.Context, eventID string) (Event, error) {
	if c == nil {
		return Event{}, fmt.Errorf("nil google calendar client")
	}
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return Event{}, fmt.Errorf("google calendar event id is required")
	}
	u := c.eventURL(eventID)
	query := u.Query()
	query.Set("fields", "id,iCalUID,etag,status,summary,description,location,organizer,attendees,start,end,created,updated,extendedProperties/private")
	u.RawQuery = query.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Event{}, fmt.Errorf("build google calendar get request: %w", err)
	}
	response, err := c.do(httpReq)
	if err != nil {
		return Event{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Event{}, c.responseError(httpReq.Method, httpReq.URL.String(), response.StatusCode)
	}
	payload, err := readBodyLimited(response.Body, c.maxBytes)
	if err != nil {
		return Event{}, fmt.Errorf("read google calendar event: %w", err)
	}
	var resource eventResource
	if err := json.Unmarshal(payload, &resource); err != nil {
		return Event{}, fmt.Errorf("decode google calendar event: %w", err)
	}
	return resource.event()
}

// Insert creates one event on the configured calendar.
func (c *Client) Insert(ctx context.Context, event Event) (Event, error) {
	if c == nil {
		return Event{}, fmt.Errorf("nil google calendar client")
	}
	body, err := json.Marshal(resourceFromEvent(event))
	if err != nil {
		return Event{}, fmt.Errorf("encode google calendar event: %w", err)
	}
	u := c.eventsURL()
	query := u.Query()
	query.Set("fields", "id,iCalUID,etag,status,summary,description,location,organizer,attendees,start,end,created,updated,extendedProperties/private")
	u.RawQuery = query.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(body)))
	if err != nil {
		return Event{}, fmt.Errorf("build google calendar insert request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")
	response, err := c.do(httpReq)
	if err != nil {
		return Event{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Event{}, c.responseError(httpReq.Method, httpReq.URL.String(), response.StatusCode)
	}
	payload, err := readBodyLimited(response.Body, c.maxBytes)
	if err != nil {
		return Event{}, fmt.Errorf("read google calendar insert response: %w", err)
	}
	var resource eventResource
	if err := json.Unmarshal(payload, &resource); err != nil {
		return Event{}, fmt.Errorf("decode google calendar insert response: %w", err)
	}
	return resource.event()
}

// Update replaces one event. When etag is set it is sent as If-Match.
func (c *Client) Update(ctx context.Context, eventID string, event Event, etag string) (Event, error) {
	if c == nil {
		return Event{}, fmt.Errorf("nil google calendar client")
	}
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return Event{}, fmt.Errorf("google calendar event id is required")
	}
	body, err := json.Marshal(resourceFromEvent(event))
	if err != nil {
		return Event{}, fmt.Errorf("encode google calendar event: %w", err)
	}
	u := c.eventURL(eventID)
	query := u.Query()
	query.Set("fields", "id,iCalUID,etag,status,summary,description,location,organizer,attendees,start,end,created,updated,extendedProperties/private")
	u.RawQuery = query.Encode()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), strings.NewReader(string(body)))
	if err != nil {
		return Event{}, fmt.Errorf("build google calendar update request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json; charset=utf-8")
	if etag = strings.TrimSpace(etag); etag != "" {
		httpReq.Header.Set("If-Match", etag)
	}
	response, err := c.do(httpReq)
	if err != nil {
		return Event{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Event{}, c.responseError(httpReq.Method, httpReq.URL.String(), response.StatusCode)
	}
	payload, err := readBodyLimited(response.Body, c.maxBytes)
	if err != nil {
		return Event{}, fmt.Errorf("read google calendar update response: %w", err)
	}
	var resource eventResource
	if err := json.Unmarshal(payload, &resource); err != nil {
		return Event{}, fmt.Errorf("decode google calendar update response: %w", err)
	}
	return resource.event()
}

// Delete removes one event. When etag is set it is sent as If-Match.
func (c *Client) Delete(ctx context.Context, eventID, etag string) error {
	if c == nil {
		return fmt.Errorf("nil google calendar client")
	}
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return fmt.Errorf("google calendar event id is required")
	}
	u := c.eventURL(eventID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, u.String(), nil)
	if err != nil {
		return fmt.Errorf("build google calendar delete request: %w", err)
	}
	if etag = strings.TrimSpace(etag); etag != "" {
		httpReq.Header.Set("If-Match", etag)
	}
	response, err := c.do(httpReq)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		return nil
	default:
		return c.responseError(httpReq.Method, httpReq.URL.String(), response.StatusCode)
	}
}

// ResponseError reports one unexpected Google Calendar API response.
type ResponseError struct {
	Method     string
	URL        string
	StatusCode int
}

func (e *ResponseError) Error() string {
	if e == nil {
		return "google calendar response error"
	}
	return fmt.Sprintf("google calendar %s %s returned %d", e.Method, e.URL, e.StatusCode)
}

type eventsListResponse struct {
	Items         []eventResource `json:"items"`
	NextPageToken string          `json:"nextPageToken"`
}

type eventResource struct {
	ID                 string        `json:"id"`
	ICalUID            string        `json:"iCalUID"`
	ETag               string        `json:"etag"`
	Status             string        `json:"status"`
	Summary            string        `json:"summary"`
	Description        string        `json:"description"`
	Location           string        `json:"location"`
	Organizer          person        `json:"organizer"`
	Attendees          []person      `json:"attendees"`
	Start              eventDateTime `json:"start"`
	End                eventDateTime `json:"end"`
	Created            string        `json:"created"`
	Updated            string        `json:"updated"`
	ExtendedProperties struct {
		Private map[string]string `json:"private"`
	} `json:"extendedProperties"`
}

type person struct {
	ID             string `json:"id,omitempty"`
	Email          string `json:"email,omitempty"`
	DisplayName    string `json:"displayName,omitempty"`
	Optional       bool   `json:"optional,omitempty"`
	ResponseStatus string `json:"responseStatus,omitempty"`
}

type eventDateTime struct {
	Date     string `json:"date,omitempty"`
	DateTime string `json:"dateTime,omitempty"`
	TimeZone string `json:"timeZone,omitempty"`
}

func (r eventResource) event() (Event, error) {
	start, startTZ, err := r.Start.parse()
	if err != nil {
		return Event{}, fmt.Errorf("parse start: %w", err)
	}
	end, endTZ, err := r.End.parse()
	if err != nil {
		return Event{}, fmt.Errorf("parse end: %w", err)
	}
	created, err := parseOptionalRFC3339(r.Created)
	if err != nil {
		return Event{}, fmt.Errorf("parse created: %w", err)
	}
	updated, err := parseOptionalRFC3339(r.Updated)
	if err != nil {
		return Event{}, fmt.Errorf("parse updated: %w", err)
	}
	timeZone := firstNonEmpty(r.Start.TimeZone, r.End.TimeZone, startTZ, endTZ)
	return Event{
		ID:                        strings.TrimSpace(r.ID),
		ICalUID:                   strings.TrimSpace(r.ICalUID),
		ETag:                      strings.TrimSpace(r.ETag),
		Status:                    strings.TrimSpace(r.Status),
		Summary:                   strings.TrimSpace(r.Summary),
		Description:               strings.TrimSpace(r.Description),
		Location:                  strings.TrimSpace(r.Location),
		Organizer:                 personParticipant(r.Organizer),
		Attendees:                 peopleParticipants(r.Attendees),
		Start:                     start.UTC(),
		End:                       end.UTC(),
		TimeZone:                  timeZone,
		Created:                   created.UTC(),
		Updated:                   updated.UTC(),
		ExtendedPropertiesPrivate: cloneStringMap(r.ExtendedProperties.Private),
	}, nil
}

func resourceFromEvent(event Event) eventResource {
	resource := eventResource{
		ID:          strings.TrimSpace(event.ID),
		ICalUID:     strings.TrimSpace(event.ICalUID),
		ETag:        strings.TrimSpace(event.ETag),
		Status:      strings.TrimSpace(event.Status),
		Summary:     strings.TrimSpace(event.Summary),
		Description: strings.TrimSpace(event.Description),
		Location:    strings.TrimSpace(event.Location),
		Organizer:   participantPerson(event.Organizer),
		Attendees:   participantsPeople(event.Attendees),
		Start:       formatEventDateTime(event.Start, event.TimeZone),
		End:         formatEventDateTime(event.End, event.TimeZone),
		Created:     formatOptionalRFC3339(event.Created),
		Updated:     formatOptionalRFC3339(event.Updated),
	}
	resource.ExtendedProperties.Private = cloneStringMap(event.ExtendedPropertiesPrivate)
	return resource
}

func (c *Client) do(httpReq *http.Request) (*http.Response, error) {
	if c.headers != nil {
		for key, values := range c.headers {
			for _, value := range values {
				httpReq.Header.Add(key, value)
			}
		}
	}
	if c.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.token)
	}
	ctx := httpReq.Context()
	cancel := func() {}
	if c.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	defer cancel()
	httpReq = httpReq.Clone(ctx)
	return c.httpClient.Do(httpReq)
}

func (c *Client) eventsURL() *url.URL {
	rel := &url.URL{Path: "/calendars/" + url.PathEscape(c.calendarID) + "/events"}
	return c.baseURL.ResolveReference(rel)
}

func (c *Client) eventURL(eventID string) *url.URL {
	rel := &url.URL{Path: "/calendars/" + url.PathEscape(c.calendarID) + "/events/" + url.PathEscape(eventID)}
	return c.baseURL.ResolveReference(rel)
}

func (c *Client) responseError(method, target string, statusCode int) error {
	err := &ResponseError{Method: method, URL: target, StatusCode: statusCode}
	switch statusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case http.StatusPreconditionFailed, http.StatusConflict:
		return fmt.Errorf("%w: %v", ErrConflict, err)
	default:
		return err
	}
}

func (e eventDateTime) parse() (time.Time, string, error) {
	if value := strings.TrimSpace(e.DateTime); value != "" {
		t, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return time.Time{}, "", err
		}
		if strings.TrimSpace(e.TimeZone) != "" {
			return t, strings.TrimSpace(e.TimeZone), nil
		}
		if name := t.Location().String(); name != "" && name != "UTC" {
			return t, name, nil
		}
		return t, "UTC", nil
	}
	if value := strings.TrimSpace(e.Date); value != "" {
		loc := time.UTC
		if tz := strings.TrimSpace(e.TimeZone); tz != "" {
			loaded, err := time.LoadLocation(tz)
			if err != nil {
				return time.Time{}, "", fmt.Errorf("load timezone %s: %w", tz, err)
			}
			loc = loaded
		}
		t, err := time.ParseInLocation("2006-01-02", value, loc)
		if err != nil {
			return time.Time{}, "", err
		}
		return t, strings.TrimSpace(e.TimeZone), nil
	}
	return time.Time{}, "", nil
}

func formatEventDateTime(value time.Time, timeZone string) eventDateTime {
	if value.IsZero() {
		return eventDateTime{}
	}
	timeZone = strings.TrimSpace(timeZone)
	if timeZone == "" || strings.EqualFold(timeZone, "UTC") {
		return eventDateTime{DateTime: value.UTC().Format(time.RFC3339), TimeZone: "UTC"}
	}
	loc, err := time.LoadLocation(timeZone)
	if err != nil {
		return eventDateTime{DateTime: value.UTC().Format(time.RFC3339), TimeZone: "UTC"}
	}
	return eventDateTime{
		DateTime: value.In(loc).Format(time.RFC3339),
		TimeZone: timeZone,
	}
}

func parseOptionalRFC3339(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, raw)
}

func formatOptionalRFC3339(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func personParticipant(p person) scheduling.Participant {
	return scheduling.Participant{
		ID:      strings.TrimSpace(p.ID),
		Name:    strings.TrimSpace(p.DisplayName),
		Address: strings.TrimSpace(p.Email),
	}
}

func peopleParticipants(items []person) []scheduling.Participant {
	if len(items) == 0 {
		return nil
	}
	out := make([]scheduling.Participant, len(items))
	for i, item := range items {
		out[i] = personParticipant(item)
	}
	return out
}

func participantPerson(p scheduling.Participant) person {
	return person{
		ID:          strings.TrimSpace(p.ID),
		Email:       strings.TrimSpace(p.Address),
		DisplayName: strings.TrimSpace(p.Name),
	}
}

func participantsPeople(items []scheduling.Participant) []person {
	if len(items) == 0 {
		return nil
	}
	out := make([]person, len(items))
	for i, item := range items {
		out[i] = participantPerson(item)
	}
	return out
}

func readBodyLimited(body io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	limited := io.LimitReader(body, maxBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("google calendar response exceeds %d bytes", maxBytes)
	}
	return payload, nil
}

func cloneHeader(header http.Header) http.Header {
	if len(header) == 0 {
		return nil
	}
	cloned := make(http.Header, len(header))
	for key, values := range header {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
