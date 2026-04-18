// Package caldavclient provides a focused CalDAV protocol client for
// scheduling adapters.
package caldavclient

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
)

const (
	defaultTimeout  = 30 * time.Second
	defaultMaxBytes = 4 * 1024 * 1024
)

var (
	// ErrNotFound reports a missing CalDAV resource.
	ErrNotFound = errors.New("caldav resource not found")
	// ErrConflict reports an ETag or precondition conflict.
	ErrConflict = errors.New("caldav conflict")
)

// Client is a focused CalDAV client over one calendar collection URL.
type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
	timeout    time.Duration
	maxBytes   int64
	username   string
	password   string
	headers    http.Header
}

// Option mutates one client configuration field.
type Option func(*Client)

// QueryRequest scopes one CalDAV calendar-query.
type QueryRequest struct {
	UID         string
	Text        string
	WindowStart time.Time
	WindowEnd   time.Time
}

// Resource is one calendar object returned by CalDAV.
type Resource struct {
	Href         string
	ETag         string
	CalendarData string
	Event        CalendarEvent
}

// PutRequest describes one CalDAV PUT.
type PutRequest struct {
	Href         string
	CalendarData string
	Event        CalendarEvent
	ETag         string
	IfNoneMatch  bool
}

// PutResult reports the resolved href and latest ETag after one PUT.
type PutResult struct {
	Href string
	ETag string
}

// CalendarEvent is the parsed VEVENT content of one calendar object.
type CalendarEvent struct {
	UID         string
	Summary     string
	Description string
	Location    string
	Organizer   scheduling.Participant
	Attendees   []scheduling.Participant
	Start       time.Time
	End         time.Time
	TimeZone    string
	Status      string
	Metadata    map[string]any
}

// New returns a CalDAV client rooted at one calendar collection URL.
func New(baseURL string, opts ...Option) (*Client, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("caldav base url is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse caldav base url: %w", err)
	}
	if !parsed.IsAbs() {
		return nil, fmt.Errorf("caldav base url must be absolute")
	}
	client := &Client{
		baseURL:    parsed,
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

// WithHTTPClient sets the HTTP client used for CalDAV requests.
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

// WithBasicAuth configures Basic authentication credentials.
func WithBasicAuth(username, password string) Option {
	return func(c *Client) {
		c.username = username
		c.password = password
	}
}

// WithHeaders clones and attaches extra headers to each request.
func WithHeaders(headers http.Header) Option {
	return func(c *Client) {
		c.headers = cloneHeader(headers)
	}
}

// Query runs one CalDAV calendar-query REPORT and returns parsed resources.
func (c *Client) Query(ctx context.Context, req QueryRequest) ([]Resource, error) {
	if c == nil {
		return nil, fmt.Errorf("nil caldav client")
	}
	body := buildCalendarQuery(req)
	request, err := http.NewRequestWithContext(ctx, "REPORT", c.baseURL.String(), strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build caldav report request: %w", err)
	}
	request.Header.Set("Content-Type", "application/xml; charset=utf-8")
	request.Header.Set("Depth", "1")
	response, err := c.do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMultiStatus && response.StatusCode != http.StatusOK {
		return nil, c.responseError(request.Method, request.URL.String(), response.StatusCode)
	}
	payload, err := readBodyLimited(response.Body, c.maxBytes)
	if err != nil {
		return nil, fmt.Errorf("read caldav report response: %w", err)
	}
	return parseMultiStatus(payload)
}

// Get loads and parses one calendar object by href.
func (c *Client) Get(ctx context.Context, href string) (Resource, error) {
	if c == nil {
		return Resource{}, fmt.Errorf("nil caldav client")
	}
	resolved, err := c.resolveHref(href)
	if err != nil {
		return Resource{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, resolved.String(), nil)
	if err != nil {
		return Resource{}, fmt.Errorf("build caldav get request: %w", err)
	}
	request.Header.Set("Accept", "text/calendar, text/plain;q=0.9, */*;q=0.1")
	response, err := c.do(request)
	if err != nil {
		return Resource{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Resource{}, c.responseError(request.Method, request.URL.String(), response.StatusCode)
	}
	payload, err := readBodyLimited(response.Body, c.maxBytes)
	if err != nil {
		return Resource{}, fmt.Errorf("read caldav resource: %w", err)
	}
	event, err := ParseCalendarData(string(payload))
	if err != nil {
		return Resource{}, fmt.Errorf("parse caldav resource %s: %w", href, err)
	}
	return Resource{
		Href:         href,
		ETag:         strings.TrimSpace(response.Header.Get("ETag")),
		CalendarData: string(payload),
		Event:        event,
	}, nil
}

// Put writes one calendar object. When req.ETag is set it is sent as
// If-Match. When req.IfNoneMatch is true the request uses If-None-Match: *.
func (c *Client) Put(ctx context.Context, req PutRequest) (PutResult, error) {
	if c == nil {
		return PutResult{}, fmt.Errorf("nil caldav client")
	}
	resolved, err := c.resolveHref(req.Href)
	if err != nil {
		return PutResult{}, err
	}
	body := req.CalendarData
	if strings.TrimSpace(body) == "" {
		body, err = FormatCalendarData(req.Event)
		if err != nil {
			return PutResult{}, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, resolved.String(), strings.NewReader(body))
	if err != nil {
		return PutResult{}, fmt.Errorf("build caldav put request: %w", err)
	}
	request.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	if etag := strings.TrimSpace(req.ETag); etag != "" {
		request.Header.Set("If-Match", etag)
	}
	if req.IfNoneMatch {
		request.Header.Set("If-None-Match", "*")
	}
	response, err := c.do(request)
	if err != nil {
		return PutResult{}, err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusCreated, http.StatusNoContent, http.StatusOK:
		return PutResult{
			Href: req.Href,
			ETag: strings.TrimSpace(response.Header.Get("ETag")),
		}, nil
	default:
		return PutResult{}, c.responseError(request.Method, request.URL.String(), response.StatusCode)
	}
}

// Delete removes one calendar object. When etag is set it is sent as
// If-Match.
func (c *Client) Delete(ctx context.Context, href, etag string) error {
	if c == nil {
		return fmt.Errorf("nil caldav client")
	}
	resolved, err := c.resolveHref(href)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, resolved.String(), nil)
	if err != nil {
		return fmt.Errorf("build caldav delete request: %w", err)
	}
	if etag = strings.TrimSpace(etag); etag != "" {
		request.Header.Set("If-Match", etag)
	}
	response, err := c.do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNoContent, http.StatusOK, http.StatusAccepted:
		return nil
	default:
		return c.responseError(request.Method, request.URL.String(), response.StatusCode)
	}
}

// ParseCalendarData parses one VCALENDAR payload with one VEVENT.
func ParseCalendarData(data string) (CalendarEvent, error) {
	lines := unfoldContentLines(data)
	var event CalendarEvent
	inEvent := false
	found := false
	for _, line := range lines {
		name, params, value, ok := parseContentLine(line)
		if !ok {
			continue
		}
		switch name {
		case "BEGIN":
			if strings.EqualFold(value, "VEVENT") {
				inEvent = true
				found = true
			}
			continue
		case "END":
			if strings.EqualFold(value, "VEVENT") {
				inEvent = false
			}
			continue
		}
		if !inEvent {
			continue
		}
		switch name {
		case "UID":
			event.UID = strings.TrimSpace(value)
		case "SUMMARY":
			event.Summary = unescapeText(value)
		case "DESCRIPTION":
			event.Description = unescapeText(value)
		case "LOCATION":
			event.Location = unescapeText(value)
		case "ORGANIZER":
			event.Organizer = parseParticipant(params, value)
		case "ATTENDEE":
			event.Attendees = append(event.Attendees, parseParticipant(params, value))
		case "DTSTART":
			t, tz, err := parseDateTime(value, params)
			if err != nil {
				return CalendarEvent{}, fmt.Errorf("parse DTSTART: %w", err)
			}
			event.Start = t
			if event.TimeZone == "" && tz != "" {
				event.TimeZone = tz
			}
		case "DTEND":
			t, tz, err := parseDateTime(value, params)
			if err != nil {
				return CalendarEvent{}, fmt.Errorf("parse DTEND: %w", err)
			}
			event.End = t
			if event.TimeZone == "" && tz != "" {
				event.TimeZone = tz
			}
		case "STATUS":
			event.Status = strings.TrimSpace(strings.ToUpper(value))
		case "X-MEMAX-METADATA":
			decoded, err := decodeMetadata(value)
			if err != nil {
				return CalendarEvent{}, fmt.Errorf("parse X-MEMAX-METADATA: %w", err)
			}
			event.Metadata = decoded
		}
	}
	if !found {
		return CalendarEvent{}, fmt.Errorf("caldav calendar data does not contain VEVENT")
	}
	if strings.TrimSpace(event.UID) == "" {
		return CalendarEvent{}, fmt.Errorf("caldav event UID is required")
	}
	return event, nil
}

// FormatCalendarData formats one VEVENT as a VCALENDAR payload.
func FormatCalendarData(event CalendarEvent) (string, error) {
	event.UID = strings.TrimSpace(event.UID)
	if event.UID == "" {
		return "", fmt.Errorf("caldav event UID is required")
	}
	if event.Start.IsZero() || event.End.IsZero() {
		return "", fmt.Errorf("caldav event start and end are required")
	}
	if !event.End.After(event.Start) {
		return "", fmt.Errorf("caldav event end must be after start")
	}
	var lines []string
	lines = append(lines,
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//Memax//CalDAV//EN",
		"BEGIN:VEVENT",
		"UID:"+event.UID,
	)
	if value := strings.TrimSpace(event.Summary); value != "" {
		lines = append(lines, "SUMMARY:"+escapeText(value))
	}
	if value := strings.TrimSpace(event.Description); value != "" {
		lines = append(lines, "DESCRIPTION:"+escapeText(value))
	}
	if value := strings.TrimSpace(event.Location); value != "" {
		lines = append(lines, "LOCATION:"+escapeText(value))
	}
	lines = append(lines, formatParticipant("ORGANIZER", event.Organizer)...)
	for _, attendee := range event.Attendees {
		lines = append(lines, formatParticipant("ATTENDEE", attendee)...)
	}
	dtStart, err := formatDateTime("DTSTART", event.Start, event.TimeZone)
	if err != nil {
		return "", err
	}
	dtEnd, err := formatDateTime("DTEND", event.End, event.TimeZone)
	if err != nil {
		return "", err
	}
	lines = append(lines, dtStart, dtEnd)
	if value := strings.TrimSpace(event.Status); value != "" {
		lines = append(lines, "STATUS:"+strings.ToUpper(value))
	}
	if len(event.Metadata) > 0 {
		encoded, err := json.Marshal(event.Metadata)
		if err != nil {
			return "", fmt.Errorf("encode caldav metadata: %w", err)
		}
		lines = append(lines, "X-MEMAX-METADATA:"+escapeText(string(encoded)))
	}
	lines = append(lines, "END:VEVENT", "END:VCALENDAR", "")
	return strings.Join(lines, "\r\n"), nil
}

type multiStatus struct {
	Responses []multiStatusResponse `xml:"response"`
}

type multiStatusResponse struct {
	Href      string                `xml:"href"`
	PropStats []multiStatusPropStat `xml:"propstat"`
}

type multiStatusPropStat struct {
	Status string `xml:"status"`
	Prop   struct {
		ETag         string `xml:"getetag"`
		CalendarData string `xml:"calendar-data"`
	} `xml:"prop"`
}

func parseMultiStatus(payload []byte) ([]Resource, error) {
	var status multiStatus
	if err := xml.Unmarshal(payload, &status); err != nil {
		return nil, fmt.Errorf("decode caldav multistatus: %w", err)
	}
	resources := make([]Resource, 0, len(status.Responses))
	for _, response := range status.Responses {
		href := strings.TrimSpace(response.Href)
		if href == "" {
			continue
		}
		var etag string
		var calendarData string
		for _, propstat := range response.PropStats {
			if !strings.Contains(propstat.Status, " 2") {
				continue
			}
			if strings.TrimSpace(propstat.Prop.ETag) != "" {
				etag = strings.TrimSpace(propstat.Prop.ETag)
			}
			if strings.TrimSpace(propstat.Prop.CalendarData) != "" {
				calendarData = propstat.Prop.CalendarData
			}
		}
		if strings.TrimSpace(calendarData) == "" {
			continue
		}
		event, err := ParseCalendarData(calendarData)
		if err != nil {
			return nil, fmt.Errorf("parse caldav resource %s: %w", href, err)
		}
		resources = append(resources, Resource{
			Href:         href,
			ETag:         etag,
			CalendarData: calendarData,
			Event:        event,
		})
	}
	return resources, nil
}

func buildCalendarQuery(req QueryRequest) string {
	var filters []string
	if uid := strings.TrimSpace(req.UID); uid != "" {
		filters = append(filters,
			`<c:prop-filter name="UID"><c:text-match collation="i;octet">`+escapeXML(uid)+`</c:text-match></c:prop-filter>`)
	}
	if text := strings.TrimSpace(req.Text); text != "" {
		filters = append(filters,
			`<c:prop-filter name="SUMMARY"><c:text-match collation="i;unicode-casemap">`+escapeXML(text)+`</c:text-match></c:prop-filter>`)
	}
	if !req.WindowStart.IsZero() || !req.WindowEnd.IsZero() {
		attrs := make([]string, 0, 2)
		if !req.WindowStart.IsZero() {
			attrs = append(attrs, `start="`+req.WindowStart.UTC().Format("20060102T150405Z")+`"`)
		}
		if !req.WindowEnd.IsZero() {
			attrs = append(attrs, `end="`+req.WindowEnd.UTC().Format("20060102T150405Z")+`"`)
		}
		filters = append(filters, `<c:time-range `+strings.Join(attrs, " ")+`/>`)
	}
	return `<?xml version="1.0" encoding="utf-8"?>` +
		`<c:calendar-query xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">` +
		`<d:prop><d:getetag/><c:calendar-data/></d:prop>` +
		`<c:filter><c:comp-filter name="VCALENDAR"><c:comp-filter name="VEVENT">` +
		strings.Join(filters, "") +
		`</c:comp-filter></c:comp-filter></c:filter>` +
		`</c:calendar-query>`
}

func (c *Client) do(request *http.Request) (*http.Response, error) {
	if c.headers != nil {
		for key, values := range c.headers {
			for _, value := range values {
				request.Header.Add(key, value)
			}
		}
	}
	if c.username != "" || c.password != "" {
		request.SetBasicAuth(c.username, c.password)
	}
	ctx := request.Context()
	cancel := func() {}
	if c.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	defer cancel()
	request = request.Clone(ctx)
	return c.httpClient.Do(request)
}

func (c *Client) resolveHref(href string) (*url.URL, error) {
	href = strings.TrimSpace(href)
	if href == "" {
		return nil, fmt.Errorf("caldav href is required")
	}
	parsed, err := url.Parse(href)
	if err != nil {
		return nil, fmt.Errorf("parse caldav href: %w", err)
	}
	return c.baseURL.ResolveReference(parsed), nil
}

func (c *Client) responseError(method, target string, statusCode int) error {
	err := &ResponseError{
		Method:     method,
		URL:        target,
		StatusCode: statusCode,
	}
	switch statusCode {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case http.StatusPreconditionFailed, http.StatusConflict:
		return fmt.Errorf("%w: %v", ErrConflict, err)
	default:
		return err
	}
}

// ResponseError reports one unexpected CalDAV HTTP response.
type ResponseError struct {
	Method     string
	URL        string
	StatusCode int
}

func (e *ResponseError) Error() string {
	if e == nil {
		return "caldav response error"
	}
	return fmt.Sprintf("caldav %s %s returned %d", e.Method, e.URL, e.StatusCode)
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
		return nil, fmt.Errorf("caldav response exceeds %d bytes", maxBytes)
	}
	return payload, nil
}

func parseParticipant(params map[string]string, value string) scheduling.Participant {
	participant := scheduling.Participant{
		Name:    strings.TrimSpace(params["CN"]),
		Role:    strings.TrimSpace(params["ROLE"]),
		Address: normalizeAddress(value),
	}
	return participant
}

func formatParticipant(name string, participant scheduling.Participant) []string {
	if strings.TrimSpace(participant.Address) == "" && strings.TrimSpace(participant.Name) == "" {
		return nil
	}
	var builder strings.Builder
	builder.WriteString(name)
	if value := strings.TrimSpace(participant.Name); value != "" {
		builder.WriteString(`;CN=`)
		builder.WriteString(escapeParam(value))
	}
	if value := strings.TrimSpace(participant.Role); value != "" {
		builder.WriteString(`;ROLE=`)
		builder.WriteString(strings.ToUpper(escapeParam(value)))
	}
	builder.WriteString(`:`)
	address := strings.TrimSpace(participant.Address)
	if address != "" && !strings.HasPrefix(strings.ToLower(address), "mailto:") {
		address = "mailto:" + address
	}
	builder.WriteString(address)
	return []string{builder.String()}
}

func parseDateTime(value string, params map[string]string) (time.Time, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, "", fmt.Errorf("empty date-time")
	}
	if strings.EqualFold(params["VALUE"], "DATE") {
		loc := time.UTC
		if tz := strings.TrimSpace(params["TZID"]); tz != "" {
			loaded, err := time.LoadLocation(tz)
			if err != nil {
				return time.Time{}, "", fmt.Errorf("load timezone %s: %w", tz, err)
			}
			loc = loaded
		}
		t, err := time.ParseInLocation("20060102", value, loc)
		return t, strings.TrimSpace(params["TZID"]), err
	}
	if strings.HasSuffix(value, "Z") {
		t, err := parseWithLayouts(value, time.UTC, "20060102T150405Z", "20060102T1504Z")
		return t, "UTC", err
	}
	if tz := strings.TrimSpace(params["TZID"]); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			return time.Time{}, "", fmt.Errorf("load timezone %s: %w", tz, err)
		}
		t, err := parseWithLayouts(value, loc, "20060102T150405", "20060102T1504")
		return t, tz, err
	}
	t, err := parseWithLayouts(value, time.UTC, "20060102T150405", "20060102T1504")
	return t, "", err
}

func parseWithLayouts(value string, loc *time.Location, layouts ...string) (time.Time, error) {
	var firstErr error
	for _, layout := range layouts {
		t, err := time.ParseInLocation(layout, value, loc)
		if err == nil {
			return t, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return time.Time{}, firstErr
}

func formatDateTime(name string, value time.Time, tz string) (string, error) {
	if value.IsZero() {
		return "", fmt.Errorf("%s is required", strings.ToLower(name))
	}
	tz = strings.TrimSpace(tz)
	if tz == "" || strings.EqualFold(tz, "UTC") {
		return name + ":" + value.UTC().Format("20060102T150405Z"), nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return "", fmt.Errorf("load timezone %s: %w", tz, err)
	}
	return name + ";TZID=" + tz + ":" + value.In(loc).Format("20060102T150405"), nil
}

func decodeMetadata(value string) (map[string]any, error) {
	var metadata map[string]any
	if err := json.Unmarshal([]byte(unescapeText(value)), &metadata); err != nil {
		return nil, err
	}
	return model.CloneMetadata(metadata), nil
}

func unfoldContentLines(data string) []string {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	raw := strings.Split(data, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if line == "" {
			continue
		}
		if len(lines) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			lines[len(lines)-1] += strings.TrimLeft(line, " \t")
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func parseContentLine(line string) (string, map[string]string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", nil, "", false
	}
	colon := strings.IndexByte(line, ':')
	if colon < 0 {
		return "", nil, "", false
	}
	head := line[:colon]
	value := line[colon+1:]
	parts := strings.Split(head, ";")
	name := strings.ToUpper(strings.TrimSpace(parts[0]))
	params := make(map[string]string, len(parts)-1)
	for _, item := range parts[1:] {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key, raw, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		params[strings.ToUpper(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(raw), `"`)
	}
	return name, params, value, true
}

func normalizeAddress(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "mailto:") {
		return value[len("mailto:"):]
	}
	return value
}

func escapeText(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"\n", `\n`,
		";", `\;`,
		",", `\,`,
	)
	return replacer.Replace(value)
}

func unescapeText(value string) string {
	replacer := strings.NewReplacer(
		`\n`, "\n",
		`\N`, "\n",
		`\,`, ",",
		`\;`, ";",
		`\\`, `\`,
	)
	return replacer.Replace(value)
}

func escapeParam(value string) string {
	if strings.ContainsAny(value, `:;,`) {
		return strconv.Quote(value)
	}
	return value
}

func escapeXML(value string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(value))
	return buf.String()
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
