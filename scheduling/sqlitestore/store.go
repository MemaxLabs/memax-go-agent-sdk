// Package sqlitestore provides a durable SQLite-backed scheduling adapter.
//
// SearchEvents intentionally returns metadata-only Event values: full
// descriptions are stored durably but left empty on search results so adapters
// honor the metadata-first contract directly. The schedule tool layer also
// formats search results without descriptions as a defensive backstop, but
// adapters should not rely on formatter redaction as their primary boundary.
package sqlitestore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/MemaxLabs/memax-go-agent-sdk/model"
	"github.com/MemaxLabs/memax-go-agent-sdk/scheduling"
)

// Store is a SQLite-backed scheduling.Searcher, Reader, Creator, Rescheduler,
// and Canceller.
type Store struct {
	db *sql.DB
}

// New initializes and returns a SQLite-backed scheduling store.
func New(ctx context.Context, db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("sqlite scheduling store db is required")
	}
	store := &Store{db: db}
	if err := store.init(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// SearchEvents returns metadata-only events ordered deterministically.
func (s *Store) SearchEvents(ctx context.Context, req scheduling.SearchRequest) ([]scheduling.Event, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("sqlite scheduling store is nil")
	}
	query := `
		SELECT id, title, summary, location, organizer_json, attendees_json,
			start_at, end_at, time_zone, status, tags_json, metadata_json
		FROM memax_schedule_events
	`
	var clauses []string
	var args []any
	if !req.WindowStart.IsZero() {
		clauses = append(clauses, "end_at > ?")
		args = append(args, formatTime(req.WindowStart.UTC()))
	}
	if !req.WindowEnd.IsZero() {
		clauses = append(clauses, "start_at < ?")
		args = append(args, formatTime(req.WindowEnd.UTC()))
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY start_at ASC, title ASC, seq ASC"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search sqlite schedule events: %w", err)
	}
	defer rows.Close()

	items := make([]scheduling.Event, 0, 16)
	for rows.Next() {
		item, err := scanEventMetadata(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sqlite schedule events: %w", err)
	}
	return (scheduling.Selector{MaxEvents: req.Limit}).Select(items, req.Query, req.WindowStart, req.WindowEnd), nil
}

// ReadEvent loads one full event by ID or title.
func (s *Store) ReadEvent(ctx context.Context, req scheduling.ReadRequest) (scheduling.Event, error) {
	if err := contextError(ctx); err != nil {
		return scheduling.Event{}, err
	}
	if s == nil {
		return scheduling.Event{}, fmt.Errorf("sqlite scheduling store is nil")
	}
	id := strings.TrimSpace(req.ID)
	if id != "" {
		item, err := s.readByQuery(ctx, `
			SELECT id, title, summary, description, location, organizer_json, attendees_json,
				start_at, end_at, time_zone, status, tags_json, metadata_json
			FROM memax_schedule_events
			WHERE id = ?
			LIMIT 1
		`, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return scheduling.Event{}, fmt.Errorf("event not found: %s", id)
			}
			return scheduling.Event{}, err
		}
		return item, nil
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		return scheduling.Event{}, fmt.Errorf("scheduling: read requires id or title")
	}
	item, err := s.readByQuery(ctx, `
		SELECT id, title, summary, description, location, organizer_json, attendees_json,
			start_at, end_at, time_zone, status, tags_json, metadata_json
		FROM memax_schedule_events
		WHERE title = ?
		ORDER BY seq ASC
		LIMIT 1
	`, title)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return scheduling.Event{}, fmt.Errorf("event not found: %s", title)
		}
		return scheduling.Event{}, err
	}
	return item, nil
}

// CreateEvent upserts one event by ID when provided, otherwise inserts a new
// event with a generated ID.
func (s *Store) CreateEvent(ctx context.Context, req scheduling.CreateRequest) (scheduling.CreateResult, error) {
	if err := contextError(ctx); err != nil {
		return scheduling.CreateResult{}, err
	}
	if s == nil {
		return scheduling.CreateResult{}, fmt.Errorf("sqlite scheduling store is nil")
	}
	item, err := normalizeCreateEvent(req.Event)
	if err != nil {
		return scheduling.CreateResult{}, err
	}
	if item.ID == "" {
		item.ID, err = newID()
		if err != nil {
			return scheduling.CreateResult{}, err
		}
	}
	record, err := encodeEventRecord(item)
	if err != nil {
		return scheduling.CreateResult{}, err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memax_schedule_events (
			id, title, summary, description, location, organizer_json, attendees_json,
			start_at, end_at, time_zone, status, tags_json, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			title = excluded.title,
			summary = excluded.summary,
			description = excluded.description,
			location = excluded.location,
			organizer_json = excluded.organizer_json,
			attendees_json = excluded.attendees_json,
			start_at = excluded.start_at,
			end_at = excluded.end_at,
			time_zone = excluded.time_zone,
			status = excluded.status,
			tags_json = excluded.tags_json,
			metadata_json = excluded.metadata_json
	`, record.args()...)
	if err != nil {
		return scheduling.CreateResult{}, fmt.Errorf("create sqlite schedule event: %w", err)
	}
	return scheduling.CreateResult{Event: item}, nil
}

// RescheduleEvent updates the timing and time zone for one event.
func (s *Store) RescheduleEvent(ctx context.Context, req scheduling.RescheduleRequest) (scheduling.RescheduleResult, error) {
	if err := contextError(ctx); err != nil {
		return scheduling.RescheduleResult{}, err
	}
	if s == nil {
		return scheduling.RescheduleResult{}, fmt.Errorf("sqlite scheduling store is nil")
	}
	if err := validateTimeRange(req.Start, req.End); err != nil {
		return scheduling.RescheduleResult{}, err
	}
	var result scheduling.RescheduleResult
	err := s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		current, err := s.readEventWithConn(ctx, conn, scheduling.ReadRequest{ID: req.ID, Title: req.Title})
		if err != nil {
			return err
		}
		previous := current
		current.Start = req.Start.UTC()
		current.End = req.End.UTC()
		if tz := strings.TrimSpace(req.TimeZone); tz != "" {
			current.TimeZone = tz
		}
		current.Metadata = mergeMetadata(current.Metadata, req.Metadata)
		record, err := encodeEventRecord(current)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `
			UPDATE memax_schedule_events
			SET title = ?, summary = ?, description = ?, location = ?, organizer_json = ?, attendees_json = ?,
				start_at = ?, end_at = ?, time_zone = ?, status = ?, tags_json = ?, metadata_json = ?
			WHERE id = ?
		`, append(record.updateArgs(), current.ID)...)
		if err != nil {
			return fmt.Errorf("reschedule sqlite schedule event: %w", err)
		}
		result = scheduling.RescheduleResult{
			Event:       current,
			Previous:    previous,
			Rescheduled: true,
		}
		return nil
	})
	if err != nil {
		return scheduling.RescheduleResult{}, err
	}
	return result, nil
}

// CancelEvent marks one event cancelled.
func (s *Store) CancelEvent(ctx context.Context, req scheduling.CancelRequest) (scheduling.CancelResult, error) {
	if err := contextError(ctx); err != nil {
		return scheduling.CancelResult{}, err
	}
	if s == nil {
		return scheduling.CancelResult{}, fmt.Errorf("sqlite scheduling store is nil")
	}
	var result scheduling.CancelResult
	err := s.withImmediateTx(ctx, func(conn *sql.Conn) error {
		current, err := s.readEventWithConn(ctx, conn, scheduling.ReadRequest{ID: req.ID, Title: req.Title})
		if err != nil {
			return err
		}
		previous := current
		current.Status = scheduling.StatusCancelled
		current.Metadata = mergeMetadata(current.Metadata, req.Metadata)
		if reason := strings.TrimSpace(req.Reason); reason != "" {
			if current.Metadata == nil {
				current.Metadata = make(map[string]any, 1)
			}
			current.Metadata["cancel_reason"] = reason
		}
		record, err := encodeEventRecord(current)
		if err != nil {
			return err
		}
		_, err = conn.ExecContext(ctx, `
			UPDATE memax_schedule_events
			SET title = ?, summary = ?, description = ?, location = ?, organizer_json = ?, attendees_json = ?,
				start_at = ?, end_at = ?, time_zone = ?, status = ?, tags_json = ?, metadata_json = ?
			WHERE id = ?
		`, append(record.updateArgs(), current.ID)...)
		if err != nil {
			return fmt.Errorf("cancel sqlite schedule event: %w", err)
		}
		result = scheduling.CancelResult{
			Event:     current,
			Previous:  previous,
			Cancelled: true,
		}
		return nil
	})
	if err != nil {
		return scheduling.CancelResult{}, err
	}
	return result, nil
}

func (s *Store) init(ctx context.Context) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS memax_schedule_events (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL UNIQUE,
			title TEXT NOT NULL,
			summary TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			location TEXT NOT NULL DEFAULT '',
			organizer_json TEXT NOT NULL DEFAULT '{}',
			attendees_json TEXT NOT NULL DEFAULT '[]',
			start_at TEXT NOT NULL,
			end_at TEXT NOT NULL,
			time_zone TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'scheduled',
			tags_json TEXT NOT NULL DEFAULT '[]',
			metadata_json TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX IF NOT EXISTS memax_schedule_events_time_idx
			ON memax_schedule_events (start_at, end_at)`,
		`CREATE INDEX IF NOT EXISTS memax_schedule_events_title_idx
			ON memax_schedule_events (title, seq)`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize sqlite scheduling store: %w", err)
		}
	}
	return nil
}

func (s *Store) readByQuery(ctx context.Context, query string, args ...any) (scheduling.Event, error) {
	row := s.db.QueryRowContext(ctx, query, args...)
	return scanEventFull(row)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readByQueryRow(ctx context.Context, q queryRower, query string, args ...any) (scheduling.Event, error) {
	row := q.QueryRowContext(ctx, query, args...)
	return scanEventFull(row)
}

func (s *Store) readEventWithConn(ctx context.Context, conn *sql.Conn, req scheduling.ReadRequest) (scheduling.Event, error) {
	id := strings.TrimSpace(req.ID)
	if id != "" {
		item, err := readByQueryRow(ctx, conn, `
			SELECT id, title, summary, description, location, organizer_json, attendees_json,
				start_at, end_at, time_zone, status, tags_json, metadata_json
			FROM memax_schedule_events
			WHERE id = ?
			LIMIT 1
		`, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return scheduling.Event{}, fmt.Errorf("event not found: %s", id)
			}
			return scheduling.Event{}, err
		}
		return item, nil
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		return scheduling.Event{}, fmt.Errorf("scheduling: read requires id or title")
	}
	item, err := readByQueryRow(ctx, conn, `
		SELECT id, title, summary, description, location, organizer_json, attendees_json,
			start_at, end_at, time_zone, status, tags_json, metadata_json
		FROM memax_schedule_events
		WHERE title = ?
		ORDER BY seq ASC
		LIMIT 1
	`, title)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return scheduling.Event{}, fmt.Errorf("event not found: %s", title)
		}
		return scheduling.Event{}, err
	}
	return item, nil
}

func (s *Store) withImmediateTx(ctx context.Context, fn func(*sql.Conn) error) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite scheduling connection: %w", err)
	}
	defer conn.Close()

	started := false
	committed := false
	defer func() {
		if started && !committed {
			_, _ = conn.ExecContext(context.Background(), "ROLLBACK")
		}
	}()

	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin sqlite scheduling transaction: %w", err)
	}
	started = true
	if err := fn(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit sqlite scheduling transaction: %w", err)
	}
	committed = true
	return nil
}

func normalizeCreateEvent(item scheduling.Event) (scheduling.Event, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Title = strings.TrimSpace(item.Title)
	item.Summary = strings.TrimSpace(item.Summary)
	item.Description = strings.TrimSpace(item.Description)
	item.Location = strings.TrimSpace(item.Location)
	item.Organizer = normalizeParticipant(item.Organizer)
	item.Attendees = normalizeParticipants(item.Attendees)
	item.Start = item.Start.UTC()
	item.End = item.End.UTC()
	item.TimeZone = strings.TrimSpace(item.TimeZone)
	item.Status = normalizeStatus(item.Status)
	item.Tags = cloneStrings(item.Tags)
	item.Metadata = model.CloneMetadata(item.Metadata)
	if err := validateCreateEvent(item); err != nil {
		return scheduling.Event{}, err
	}
	return item, nil
}

func validateCreateEvent(item scheduling.Event) error {
	if strings.TrimSpace(item.Title) == "" {
		return fmt.Errorf("scheduling: title is required")
	}
	if err := validateTimeRange(item.Start, item.End); err != nil {
		return err
	}
	if len(item.Attendees) == 0 && participantKey(item.Organizer) == "" {
		return fmt.Errorf("scheduling: organizer or attendee is required")
	}
	return nil
}

func validateTimeRange(start, end time.Time) error {
	if start.IsZero() || end.IsZero() {
		return fmt.Errorf("scheduling: start and end are required")
	}
	if !end.After(start) {
		return fmt.Errorf("scheduling: end must be after start")
	}
	return nil
}

func normalizeStatus(status scheduling.Status) scheduling.Status {
	status = scheduling.Status(strings.TrimSpace(string(status)))
	if status == "" {
		return scheduling.StatusScheduled
	}
	return status
}

func normalizeParticipant(item scheduling.Participant) scheduling.Participant {
	return scheduling.Participant{
		ID:      strings.TrimSpace(item.ID),
		Name:    strings.TrimSpace(item.Name),
		Address: strings.TrimSpace(item.Address),
		Role:    strings.TrimSpace(item.Role),
	}
}

func normalizeParticipants(items []scheduling.Participant) []scheduling.Participant {
	if len(items) == 0 {
		return nil
	}
	out := make([]scheduling.Participant, len(items))
	for i, item := range items {
		out[i] = normalizeParticipant(item)
	}
	return out
}

func mergeMetadata(existing, extra map[string]any) map[string]any {
	if len(existing) == 0 && len(extra) == 0 {
		return nil
	}
	merged := model.CloneMetadata(existing)
	if merged == nil {
		merged = make(map[string]any, len(extra))
	}
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func participantKey(item scheduling.Participant) string {
	switch {
	case item.Address != "":
		return strings.ToLower(item.Address)
	case item.Name != "":
		return strings.ToLower(item.Name)
	default:
		return strings.ToLower(item.ID)
	}
}

func cloneStrings(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	return append([]string(nil), items...)
}

type eventRecord struct {
	id          string
	title       string
	summary     string
	description string
	location    string
	organizer   string
	attendees   string
	startAt     string
	endAt       string
	timeZone    string
	status      string
	tags        string
	metadata    string
}

func encodeEventRecord(item scheduling.Event) (eventRecord, error) {
	organizer, err := json.Marshal(item.Organizer)
	if err != nil {
		return eventRecord{}, fmt.Errorf("encode organizer: %w", err)
	}
	attendees, err := json.Marshal(item.Attendees)
	if err != nil {
		return eventRecord{}, fmt.Errorf("encode attendees: %w", err)
	}
	tags, err := json.Marshal(item.Tags)
	if err != nil {
		return eventRecord{}, fmt.Errorf("encode tags: %w", err)
	}
	metadata, err := json.Marshal(item.Metadata)
	if err != nil {
		return eventRecord{}, fmt.Errorf("encode metadata: %w", err)
	}
	return eventRecord{
		id:          item.ID,
		title:       item.Title,
		summary:     item.Summary,
		description: item.Description,
		location:    item.Location,
		organizer:   string(organizer),
		attendees:   string(attendees),
		startAt:     formatTime(item.Start),
		endAt:       formatTime(item.End),
		timeZone:    item.TimeZone,
		status:      string(item.Status),
		tags:        string(tags),
		metadata:    string(metadata),
	}, nil
}

func (r eventRecord) args() []any {
	return []any{
		r.id,
		r.title,
		r.summary,
		r.description,
		r.location,
		r.organizer,
		r.attendees,
		r.startAt,
		r.endAt,
		r.timeZone,
		r.status,
		r.tags,
		r.metadata,
	}
}

func (r eventRecord) updateArgs() []any {
	return []any{
		r.title,
		r.summary,
		r.description,
		r.location,
		r.organizer,
		r.attendees,
		r.startAt,
		r.endAt,
		r.timeZone,
		r.status,
		r.tags,
		r.metadata,
	}
}

func scanEventMetadata(rows *sql.Rows) (scheduling.Event, error) {
	var (
		item         scheduling.Event
		organizerRaw string
		attendeesRaw string
		startAt      string
		endAt        string
		tagsRaw      string
		metadataRaw  string
	)
	if err := rows.Scan(
		&item.ID,
		&item.Title,
		&item.Summary,
		&item.Location,
		&organizerRaw,
		&attendeesRaw,
		&startAt,
		&endAt,
		&item.TimeZone,
		&item.Status,
		&tagsRaw,
		&metadataRaw,
	); err != nil {
		return scheduling.Event{}, fmt.Errorf("scan sqlite schedule metadata: %w", err)
	}
	if err := decodeEventFields(&item, organizerRaw, attendeesRaw, startAt, endAt, tagsRaw, metadataRaw); err != nil {
		return scheduling.Event{}, err
	}
	item.Description = ""
	return item, nil
}

func scanEventFull(row *sql.Row) (scheduling.Event, error) {
	var (
		item         scheduling.Event
		organizerRaw string
		attendeesRaw string
		startAt      string
		endAt        string
		tagsRaw      string
		metadataRaw  string
	)
	if err := row.Scan(
		&item.ID,
		&item.Title,
		&item.Summary,
		&item.Description,
		&item.Location,
		&organizerRaw,
		&attendeesRaw,
		&startAt,
		&endAt,
		&item.TimeZone,
		&item.Status,
		&tagsRaw,
		&metadataRaw,
	); err != nil {
		return scheduling.Event{}, err
	}
	if err := decodeEventFields(&item, organizerRaw, attendeesRaw, startAt, endAt, tagsRaw, metadataRaw); err != nil {
		return scheduling.Event{}, err
	}
	return item, nil
}

func decodeEventFields(item *scheduling.Event, organizerRaw, attendeesRaw, startAt, endAt, tagsRaw, metadataRaw string) error {
	if organizerRaw != "" {
		if err := json.Unmarshal([]byte(organizerRaw), &item.Organizer); err != nil {
			return fmt.Errorf("decode sqlite schedule organizer: %w", err)
		}
	}
	if attendeesRaw != "" {
		if err := json.Unmarshal([]byte(attendeesRaw), &item.Attendees); err != nil {
			return fmt.Errorf("decode sqlite schedule attendees: %w", err)
		}
	}
	if startAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, startAt)
		if err != nil {
			return fmt.Errorf("parse sqlite schedule start_at: %w", err)
		}
		item.Start = parsed
	}
	if endAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, endAt)
		if err != nil {
			return fmt.Errorf("parse sqlite schedule end_at: %w", err)
		}
		item.End = parsed
	}
	if tagsRaw != "" {
		if err := json.Unmarshal([]byte(tagsRaw), &item.Tags); err != nil {
			return fmt.Errorf("decode sqlite schedule tags: %w", err)
		}
	}
	if metadataRaw != "" {
		if err := json.Unmarshal([]byte(metadataRaw), &item.Metadata); err != nil {
			return fmt.Errorf("decode sqlite schedule metadata: %w", err)
		}
	}
	return nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate schedule event id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
