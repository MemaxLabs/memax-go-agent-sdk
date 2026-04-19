// Package jmapstore adapts JMAP mail backends to the messaging contracts.
//
// SearchThreads returns metadata-only messaging.Thread values. Full message
// content remains available through ReadThread, while the message tool layer
// still formats search results without thread bodies as a defensive backstop.
// Outbound send uses Email/set followed by EmailSubmission/set, then reloads
// the persisted email so SendResult reflects the stored message shape rather
// than a fire-and-forget submission stub.
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
	client            *jmapclient.Client
	maxBodyBytes      int
	defaultIdentityID string
	defaultSender     jmapclient.EmailAddress
	draftMailboxID    string
}

// Option mutates one store configuration field.
type Option func(*Store)

// WithMaxBodyBytes sets the maximum text body bytes fetched per email on read.
func WithMaxBodyBytes(max int) Option {
	return func(s *Store) {
		s.maxBodyBytes = max
	}
}

// WithDefaultIdentity configures the JMAP identity used for submissions.
func WithDefaultIdentity(id string) Option {
	return func(s *Store) {
		s.defaultIdentityID = strings.TrimSpace(id)
	}
}

// WithDefaultSender configures the From header applied to created emails.
func WithDefaultSender(name, address string) Option {
	return func(s *Store) {
		s.defaultSender = jmapclient.EmailAddress{
			Name:  strings.TrimSpace(name),
			Email: strings.TrimSpace(address),
		}
	}
}

// WithDraftMailbox configures the mailbox used for the created draft email
// before submission.
func WithDraftMailbox(id string) Option {
	return func(s *Store) {
		s.draftMailboxID = strings.TrimSpace(id)
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
		Filter:          filter(req.Filter),
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

// SendMessage creates and submits one outbound JMAP email, then reloads the
// persisted message shape.
func (s *Store) SendMessage(ctx context.Context, req messaging.SendRequest) (messaging.SendResult, error) {
	if err := contextError(ctx); err != nil {
		return messaging.SendResult{}, err
	}
	if s == nil {
		return messaging.SendResult{}, fmt.Errorf("jmap messaging store is nil")
	}
	identityID := strings.TrimSpace(s.defaultIdentityID)
	if identityID == "" {
		return messaging.SendResult{}, fmt.Errorf("jmap messaging store default identity is required for send")
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return messaging.SendResult{}, fmt.Errorf("messaging: send requires body")
	}
	recipients := emailAddresses(req.Recipients)
	if len(recipients) == 0 {
		return messaging.SendResult{}, fmt.Errorf("messaging: send requires at least one recipient")
	}
	subject, err := s.sendSubject(ctx, req)
	if err != nil {
		return messaging.SendResult{}, err
	}
	create := jmapclient.EmailCreateRequest{
		CreateID:   "email",
		MailboxIDs: s.sendMailboxIDs(),
		Keywords:   s.sendKeywords(),
		Subject:    subject,
		From:       s.sendFrom(),
		To:         recipients,
		TextBody:   body,
	}
	created, err := s.client.CreateEmail(ctx, create)
	if err != nil {
		return messaging.SendResult{}, fmt.Errorf("create jmap email: %w", err)
	}
	submitted, err := s.client.SubmitEmail(ctx, jmapclient.EmailSubmissionRequest{
		CreateID:             "submission",
		EmailID:              created.EmailID,
		IdentityID:           identityID,
		OnSuccessUpdateEmail: s.onSuccessUpdateEmail(created.EmailID),
	})
	if err != nil {
		return messaging.SendResult{}, fmt.Errorf("submit jmap email: %w", err)
	}
	thread, err := s.loadSentThread(ctx, req.ThreadID, submitted.EmailID)
	if err != nil {
		return messaging.SendResult{}, err
	}
	message := findMessage(thread, submitted.EmailID)
	if message.ID == "" {
		// Some JMAP servers expose submission success before the reloaded thread
		// reflects the new message. In that eventual-consistency fallback, return
		// a synthetic outbound message reconstructed from the request so the send
		// still has a coherent result shape, but note that server-populated
		// fields such as the final sent timestamp may be missing.
		message = messaging.Message{
			ID:         submitted.EmailID,
			ThreadID:   thread.ID,
			Subject:    thread.Subject,
			Summary:    thread.Summary,
			Body:       body,
			Direction:  messaging.DirectionOutbound,
			Sender:     firstParticipant(s.sendFrom(), "from"),
			Recipients: participantRecipients(req.Recipients),
			Metadata: map[string]any{
				metadataAdapter: "jmap",
			},
		}
	}
	return messaging.SendResult{
		Thread:        thread,
		Message:       message,
		CreatedThread: strings.TrimSpace(req.ThreadID) == "",
	}, nil
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
		Filter:          jmapclient.Filter{},
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
	if item.Keywords["$draft"] || item.Keywords["$sent"] {
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
var _ messaging.Sender = (*Store)(nil)

func filter(src messaging.SearchFilter) jmapclient.Filter {
	return jmapclient.Filter{
		Mailboxes: append([]string(nil), src.Mailboxes...),
		From:      append([]string(nil), src.From...),
		Since:     src.Since,
		Until:     src.Until,
		Unread:    src.Unread,
	}
}

func emailAddresses(items []messaging.Participant) []jmapclient.EmailAddress {
	if len(items) == 0 {
		return nil
	}
	out := make([]jmapclient.EmailAddress, 0, len(items))
	for _, item := range items {
		address := strings.TrimSpace(item.Address)
		name := strings.TrimSpace(item.Name)
		if address == "" && name == "" {
			continue
		}
		out = append(out, jmapclient.EmailAddress{Name: name, Email: address})
	}
	return out
}

func participantRecipients(items []messaging.Participant) []messaging.Participant {
	if len(items) == 0 {
		return nil
	}
	out := make([]messaging.Participant, 0, len(items))
	for _, item := range items {
		clone := messaging.Participant{
			ID:      strings.TrimSpace(item.ID),
			Name:    strings.TrimSpace(item.Name),
			Address: strings.TrimSpace(item.Address),
			Role:    firstNonEmpty(strings.TrimSpace(item.Role), "to"),
		}
		if clone.Name == "" && clone.Address == "" {
			continue
		}
		out = append(out, clone)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func findMessage(thread messaging.Thread, messageID string) messaging.Message {
	messageID = strings.TrimSpace(messageID)
	for _, item := range thread.Messages {
		if strings.TrimSpace(item.ID) == messageID {
			return item
		}
	}
	return messaging.Message{}
}

func (s *Store) sendSubject(ctx context.Context, req messaging.SendRequest) (string, error) {
	subject := strings.TrimSpace(req.Subject)
	if subject != "" {
		return subject, nil
	}
	if strings.TrimSpace(req.ThreadID) == "" {
		return "", fmt.Errorf("messaging: send requires subject for a new thread")
	}
	thread, err := s.ReadThread(ctx, messaging.ReadRequest{ThreadID: req.ThreadID})
	if err != nil {
		return "", fmt.Errorf("resolve send subject from thread %s: %w", strings.TrimSpace(req.ThreadID), err)
	}
	subject = strings.TrimSpace(thread.Subject)
	if subject == "" {
		return "", fmt.Errorf("messaging: send requires subject")
	}
	return subject, nil
}

func (s *Store) sendMailboxIDs() map[string]bool {
	if strings.TrimSpace(s.draftMailboxID) == "" {
		return nil
	}
	return map[string]bool{s.draftMailboxID: true}
}

func (s *Store) sendKeywords() map[string]bool {
	keywords := map[string]bool{"$draft": true}
	return keywords
}

func (s *Store) sendFrom() []jmapclient.EmailAddress {
	if strings.TrimSpace(s.defaultSender.Name) == "" && strings.TrimSpace(s.defaultSender.Email) == "" {
		return nil
	}
	return []jmapclient.EmailAddress{s.defaultSender}
}

func (s *Store) onSuccessUpdateEmail(emailID string) map[string]any {
	emailID = strings.TrimSpace(emailID)
	if emailID == "" {
		return nil
	}
	patch := map[string]any{
		"keywords/$draft": nil,
		"keywords/$sent":  true,
	}
	if mailboxID := strings.TrimSpace(s.draftMailboxID); mailboxID != "" {
		patch["mailboxIds/"+mailboxID] = nil
	}
	return map[string]any{
		emailID: patch,
	}
}

func (s *Store) loadSentThread(ctx context.Context, existingThreadID, emailID string) (messaging.Thread, error) {
	items, err := s.client.GetEmails(ctx, jmapclient.EmailGetRequest{
		IDs:                 []string{emailID},
		Properties:          readProperties(),
		FetchTextBodyValues: true,
		FetchHTMLBodyValues: true,
		MaxBodyValueBytes:   s.maxBodyBytes,
	})
	if err != nil {
		return messaging.Thread{}, fmt.Errorf("read jmap sent email %s: %w", emailID, err)
	}
	if len(items) == 0 {
		return messaging.Thread{}, fmt.Errorf("sent email not found: %s", emailID)
	}
	threadID := firstNonEmpty(items[0].ThreadID, existingThreadID, items[0].ID)
	thread, err := s.ReadThread(ctx, messaging.ReadRequest{ThreadID: threadID})
	if err == nil {
		return thread, nil
	}
	thread = threadFromEmails(threadID, items, true)
	if thread.ID == "" {
		thread.ID = threadID
	}
	return thread, nil
}
