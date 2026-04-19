// Package jmapstore adapts JMAP mail backends to the messaging contracts.
//
// SearchThreads returns metadata-only messaging.Thread values. Full message
// content remains available through ReadThread, while the message tool layer
// still formats search results without thread bodies as a defensive backstop.
// This adapter is currently read-only; hosts that need outbound mail should
// attach a separate messaging.Sender until JMAP EmailSubmission support lands.
package jmapstore

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/MemaxLabs/memax-go-agent-sdk/messaging"
	"github.com/MemaxLabs/memax-go-agent-sdk/messaging/jmapclient"
)

const (
	metadataAdapter = "adapter"
	// JMAP does not expose per-message or per-thread ETags in the same shape as
	// the scheduling backends, so the latest email ID is the adapter-specific
	// anchor for hosts that need incremental follow-up reads.
	metadataLatestEmailID = "jmap_email_id"

	defaultSearchLimit    = 8
	defaultMaxBodyBytes   = 64 * 1024
	defaultSubjectMatches = 20
)

// Store adapts one JMAP account to messaging.Searcher and messaging.Reader.
type Store struct {
	client       *jmapclient.Client
	maxBodyBytes int
}

// Option mutates one store configuration field.
type Option func(*Store)

// WithMaxBodyBytes sets the maximum text body bytes fetched per email on read.
func WithMaxBodyBytes(max int) Option {
	return func(s *Store) {
		s.maxBodyBytes = max
	}
}

// New returns a messaging store over one JMAP client.
func New(client *jmapclient.Client, opts ...Option) (*Store, error) {
	if client == nil {
		return nil, fmt.Errorf("jmap messaging client is required")
	}
	store := &Store{
		client:       client,
		maxBodyBytes: defaultMaxBodyBytes,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(store)
		}
	}
	if store.maxBodyBytes <= 0 {
		store.maxBodyBytes = defaultMaxBodyBytes
	}
	return store, nil
}

// SearchThreads searches message-thread metadata without loading full message
// content.
func (s *Store) SearchThreads(ctx context.Context, req messaging.SearchRequest) ([]messaging.Thread, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("jmap messaging store is nil")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	ids, err := s.client.QueryEmails(ctx, jmapclient.QueryRequest{
		Text:            strings.TrimSpace(req.Query),
		Limit:           limit,
		CollapseThreads: true,
	})
	if err != nil {
		return nil, fmt.Errorf("search jmap message threads: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	items, err := s.client.GetEmails(ctx, jmapclient.EmailGetRequest{
		IDs:        ids,
		Properties: searchProperties(),
	})
	if err != nil {
		return nil, fmt.Errorf("read jmap message thread metadata: %w", err)
	}
	threads := make([]messaging.Thread, 0, len(items))
	for _, item := range items {
		thread := threadFromEmails(item.ThreadID, []jmapclient.Email{item}, false)
		if thread.ID == "" {
			thread.ID = item.ThreadID
		}
		if thread.ID == "" {
			thread.ID = item.ID
		}
		threads = append(threads, thread)
	}
	return threads, nil
}

// ReadThread loads one full message thread by thread ID or exact subject.
func (s *Store) ReadThread(ctx context.Context, req messaging.ReadRequest) (messaging.Thread, error) {
	if err := contextError(ctx); err != nil {
		return messaging.Thread{}, err
	}
	if s == nil {
		return messaging.Thread{}, fmt.Errorf("jmap messaging store is nil")
	}
	threadID, err := s.resolveThreadID(ctx, req)
	if err != nil {
		return messaging.Thread{}, err
	}
	threads, err := s.client.GetThreads(ctx, []string{threadID})
	if err != nil {
		return messaging.Thread{}, fmt.Errorf("read jmap thread %s: %w", threadID, err)
	}
	if len(threads) == 0 || len(threads[0].EmailIDs) == 0 {
		return messaging.Thread{}, fmt.Errorf("thread not found: %s", threadID)
	}
	items, err := s.client.GetEmails(ctx, jmapclient.EmailGetRequest{
		IDs:                 threads[0].EmailIDs,
		Properties:          readProperties(),
		FetchTextBodyValues: true,
		FetchHTMLBodyValues: true,
		MaxBodyValueBytes:   s.maxBodyBytes,
	})
	if err != nil {
		return messaging.Thread{}, fmt.Errorf("read jmap thread messages %s: %w", threadID, err)
	}
	byID := make(map[string]jmapclient.Email, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	ordered := make([]jmapclient.Email, 0, len(threads[0].EmailIDs))
	for _, id := range threads[0].EmailIDs {
		if item, ok := byID[id]; ok {
			ordered = append(ordered, item)
		}
	}
	if len(ordered) == 0 {
		return messaging.Thread{}, fmt.Errorf("thread not found: %s", threadID)
	}
	thread := threadFromEmails(threadID, ordered, true)
	if thread.ID == "" {
		thread.ID = threadID
	}
	return thread, nil
}

func (s *Store) resolveThreadID(ctx context.Context, req messaging.ReadRequest) (string, error) {
	if id := strings.TrimSpace(req.ThreadID); id != "" {
		return id, nil
	}
	subject := strings.TrimSpace(req.Subject)
	if subject == "" {
		return "", fmt.Errorf("messaging: read requires thread_id or subject")
	}
	ids, err := s.client.QueryEmails(ctx, jmapclient.QueryRequest{
		Text:            subject,
		Limit:           defaultSubjectMatches,
		CollapseThreads: true,
	})
	if err != nil {
		return "", fmt.Errorf("search jmap message threads for %s: %w", subject, err)
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("thread not found: %s", subject)
	}
	items, err := s.client.GetEmails(ctx, jmapclient.EmailGetRequest{
		IDs:        ids,
		Properties: searchProperties(),
	})
	if err != nil {
		return "", fmt.Errorf("read jmap message thread metadata %s: %w", subject, err)
	}
	matches := make([]jmapclient.Email, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Subject) == subject {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("thread not found: %s", subject)
	}
	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].ReceivedAt.Equal(matches[j].ReceivedAt) {
			return matches[i].ReceivedAt.After(matches[j].ReceivedAt)
		}
		return matches[i].ID < matches[j].ID
	})
	return strings.TrimSpace(matches[0].ThreadID), nil
}

func searchProperties() []string {
	return []string{
		"id",
		"threadId",
		"mailboxIds",
		"keywords",
		"subject",
		"preview",
		"receivedAt",
		"from",
		"to",
		"cc",
		"bcc",
	}
}

func readProperties() []string {
	return []string{
		"id",
		"threadId",
		"mailboxIds",
		"keywords",
		"subject",
		"preview",
		"receivedAt",
		"from",
		"to",
		"cc",
		"bcc",
		"textBody",
		"htmlBody",
		"bodyValues",
	}
}

func threadFromEmails(threadID string, items []jmapclient.Email, includeMessages bool) messaging.Thread {
	latest := latestEmail(items)
	thread := messaging.Thread{
		ID:            strings.TrimSpace(threadID),
		Subject:       strings.TrimSpace(latest.Subject),
		Summary:       strings.TrimSpace(latest.Preview),
		Snippet:       strings.TrimSpace(latest.Preview),
		Participants:  collectParticipants(items),
		Tags:          collectTags(items),
		LastMessageAt: latest.ReceivedAt.UTC(),
		Metadata: map[string]any{
			metadataAdapter: "jmap",
		},
	}
	if latest.ID != "" {
		thread.Metadata[metadataLatestEmailID] = latest.ID
	}
	if includeMessages {
		thread.Messages = messagesFromEmails(thread.ID, items)
	}
	return thread
}

func messagesFromEmails(threadID string, items []jmapclient.Email) []messaging.Message {
	if len(items) == 0 {
		return nil
	}
	out := make([]messaging.Message, 0, len(items))
	for _, item := range items {
		recipients := make([]messaging.Participant, 0, len(item.To)+len(item.CC)+len(item.BCC))
		recipients = appendParticipants(recipients, item.To, "to")
		recipients = appendParticipants(recipients, item.CC, "cc")
		recipients = appendParticipants(recipients, item.BCC, "bcc")
		out = append(out, messaging.Message{
			ID:         item.ID,
			ThreadID:   threadID,
			Subject:    item.Subject,
			Summary:    item.Preview,
			Body:       bodyText(item),
			Direction:  direction(item),
			Sender:     firstParticipant(item.From, "from"),
			Recipients: recipients,
			SentAt:     item.ReceivedAt,
			Metadata: map[string]any{
				metadataAdapter: "jmap",
			},
		})
	}
	return out
}

func direction(item jmapclient.Email) messaging.Direction {
	if item.Keywords["$draft"] {
		return messaging.DirectionOutbound
	}
	return messaging.DirectionInbound
}

func bodyText(item jmapclient.Email) string {
	if text := joinBodyValues(item.TextBody, item.BodyValues); text != "" {
		return text
	}
	if html := joinBodyValues(item.HTMLBody, item.BodyValues); html != "" {
		return html
	}
	return strings.TrimSpace(item.Preview)
}

func joinBodyValues(parts []jmapclient.BodyPart, values map[string]jmapclient.BodyValue) string {
	if len(parts) == 0 || len(values) == 0 {
		return ""
	}
	var out []string
	for _, part := range parts {
		partID := strings.TrimSpace(part.PartID)
		if partID == "" {
			continue
		}
		value := strings.TrimSpace(values[partID].Value)
		if value != "" {
			out = append(out, value)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n\n"))
}

func latestEmail(items []jmapclient.Email) jmapclient.Email {
	if len(items) == 0 {
		return jmapclient.Email{}
	}
	latest := items[0]
	for _, item := range items[1:] {
		if item.ReceivedAt.After(latest.ReceivedAt) {
			latest = item
			continue
		}
		if item.ReceivedAt.Equal(latest.ReceivedAt) && item.ID > latest.ID {
			latest = item
		}
	}
	return latest
}

func collectParticipants(items []jmapclient.Email) []messaging.Participant {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []messaging.Participant
	for _, item := range items {
		out = appendUniqueParticipants(out, seen, item.From, "from")
		out = appendUniqueParticipants(out, seen, item.To, "to")
		out = appendUniqueParticipants(out, seen, item.CC, "cc")
		out = appendUniqueParticipants(out, seen, item.BCC, "bcc")
	}
	return out
}

func collectTags(items []jmapclient.Email) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, item := range items {
		for key, enabled := range item.Keywords {
			key = strings.TrimSpace(key)
			if !enabled || key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func appendParticipants(dst []messaging.Participant, items []jmapclient.EmailAddress, role string) []messaging.Participant {
	for _, item := range items {
		participant := messaging.Participant{
			Name:    strings.TrimSpace(item.Name),
			Address: strings.TrimSpace(item.Email),
			Role:    role,
		}
		if participant.Name == "" && participant.Address == "" {
			continue
		}
		dst = append(dst, participant)
	}
	return dst
}

func appendUniqueParticipants(dst []messaging.Participant, seen map[string]struct{}, items []jmapclient.EmailAddress, role string) []messaging.Participant {
	for _, item := range items {
		participant := messaging.Participant{
			Name:    strings.TrimSpace(item.Name),
			Address: strings.TrimSpace(item.Email),
			Role:    role,
		}
		if participant.Name == "" && participant.Address == "" {
			continue
		}
		key := participantKey(participant)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		dst = append(dst, participant)
	}
	return dst
}

func firstParticipant(items []jmapclient.EmailAddress, role string) messaging.Participant {
	if len(items) == 0 {
		return messaging.Participant{}
	}
	return messaging.Participant{
		Name:    strings.TrimSpace(items[0].Name),
		Address: strings.TrimSpace(items[0].Email),
		Role:    role,
	}
}

func participantKey(item messaging.Participant) string {
	switch {
	case item.Address != "":
		return strings.ToLower(item.Address)
	case item.Name != "":
		return strings.ToLower(item.Name)
	default:
		return item.Role
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

var _ messaging.Searcher = (*Store)(nil)
var _ messaging.Reader = (*Store)(nil)
