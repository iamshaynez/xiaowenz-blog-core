package store

import (
	"strconv"
	"testing"
	"time"
)

func TestSubscriberLifecycle(t *testing.T) {
	s := newTestStore(t)

	// Create pending subscriber.
	sub, err := s.UpsertPending("Alice@Example.com", "tok1", time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("UpsertPending: %v", err)
	}
	if sub.Email != "alice@example.com" {
		t.Errorf("email not normalized: %q", sub.Email)
	}
	if sub.Status != "pending" {
		t.Errorf("status = %q, want pending", sub.Status)
	}

	// Same email again (pending) refreshes token instead of duplicating.
	sub2, err := s.UpsertPending("alice@example.com", "tok2", time.Now().Add(48*time.Hour))
	if err != nil {
		t.Fatalf("UpsertPending again: %v", err)
	}
	if sub2.ID != sub.ID {
		t.Errorf("duplicate row created: ids %d vs %d", sub.ID, sub2.ID)
	}
	if sub2.ConfirmToken != "tok2" {
		t.Errorf("token not refreshed: %q", sub2.ConfirmToken)
	}

	// Confirm by token.
	got, err := s.ConfirmSubscriber("tok2")
	if err != nil {
		t.Fatalf("ConfirmSubscriber: %v", err)
	}
	if got.Status != "active" {
		t.Errorf("status = %q, want active", got.Status)
	}
	if got.ConfirmedAt == 0 {
		t.Error("confirmed_at not set")
	}

	// Unknown token must error.
	if _, err := s.ConfirmSubscriber("nope"); err == nil {
		t.Error("ConfirmSubscriber with bad token must fail")
	}

	// Active subscriber re-subscribing stays active (handled at handler level),
	// but store must expose lookup by email.
	byEmail, err := s.GetSubscriberByEmail("ALICE@example.com")
	if err != nil {
		t.Fatalf("GetSubscriberByEmail: %v", err)
	}
	if byEmail.Status != "active" {
		t.Errorf("status = %q, want active", byEmail.Status)
	}

	// Unsubscribe via unsub token.
	if err := s.UnsubscribeByToken(byEmail.UnsubToken); err != nil {
		t.Fatalf("UnsubscribeByToken: %v", err)
	}
	after, _ := s.GetSubscriberByEmail("alice@example.com")
	if after.Status != "unsubscribed" {
		t.Errorf("status = %q, want unsubscribed", after.Status)
	}
}

func TestUnsubscribeByBadToken(t *testing.T) {
	s := newTestStore(t)
	if err := s.UnsubscribeByToken("missing"); err == nil {
		t.Fatal("expected error for unknown unsub token")
	}
}

func TestConfirmExpiredTokenRejected(t *testing.T) {
	s := newTestStore(t)
	past := time.Now().Add(-time.Hour)
	if _, err := s.UpsertPending("old@example.com", "oldtok", past); err != nil {
		t.Fatalf("UpsertPending: %v", err)
	}
	if _, err := s.ConfirmSubscriber("oldtok"); err == nil {
		t.Fatal("expired confirm token must be rejected")
	}
}

func TestListAndCountSubscribers(t *testing.T) {
	s := newTestStore(t)
	exp := time.Now().Add(24 * time.Hour)
	names := []string{"a@x.com", "b@x.com", "c@y.com"}
	for _, e := range names {
		sub, err := s.UpsertPending(e, "tok-"+e, exp)
		if err != nil {
			t.Fatalf("UpsertPending: %v", err)
		}
		if err := s.Activate(sub.ID); err != nil {
			t.Fatalf("Activate: %v", err)
		}
	}

	list, err := s.ListSubscribers("active", "", 0, 50)
	if err != nil {
		t.Fatalf("ListSubscribers: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("len = %d, want 3", len(list))
	}
	n, err := s.CountSubscribers("active", "")
	if err != nil {
		t.Fatalf("CountSubscribers: %v", err)
	}
	if n != 3 {
		t.Errorf("count = %d, want 3", n)
	}

	// Search filter.
	q, err := s.ListSubscribers("", "y.com", 0, 50)
	if err != nil {
		t.Fatalf("ListSubscribers q: %v", err)
	}
	if len(q) != 1 || q[0].Email != "c@y.com" {
		t.Errorf("search returned %+v", q)
	}

	// Active list for sending.
	act, err := s.ActiveSubscribers()
	if err != nil {
		t.Fatalf("ActiveSubscribers: %v", err)
	}
	if len(act) != 3 {
		t.Errorf("active = %d, want 3", len(act))
	}

	// Delete.
	if err := s.DeleteSubscriber(list[0].ID); err != nil {
		t.Fatalf("DeleteSubscriber: %v", err)
	}
	n, _ = s.CountSubscribers("", "")
	if n != 2 {
		t.Errorf("count after delete = %d, want 2", n)
	}
}

func TestImportSubscribersSkipsDuplicates(t *testing.T) {
	s := newTestStore(t)
	exp := time.Now().Add(24 * time.Hour)
	sub, err := s.UpsertPending("dup@x.com", "tok", exp)
	if err != nil {
		t.Fatalf("UpsertPending: %v", err)
	}
	if err := s.Activate(sub.ID); err != nil {
		t.Fatalf("Activate: %v", err)
	}

	imported, skipped, invalid, err := s.ImportSubscribers([]string{"dup@x.com", "New@X.com", "second@x.com"})
	if err != nil {
		t.Fatalf("ImportSubscribers: %v", err)
	}
	if imported != 2 || skipped != 1 || invalid != 0 {
		t.Errorf("imported=%d skipped=%d invalid=%d, want 2/1/0", imported, skipped, invalid)
	}
	got, err := s.GetSubscriberByEmail("new@x.com")
	if err != nil {
		t.Fatalf("GetSubscriberByEmail: %v", err)
	}
	// Import = already verified elsewhere: active immediately with confirmed_at.
	if got.Status != "active" || got.ConfirmedAt == 0 {
		t.Errorf("imported subscriber: status=%q confirmed_at=%d", got.Status, got.ConfirmedAt)
	}
	if got.UnsubToken == "" {
		t.Error("imported subscriber has no unsub token")
	}
}

func TestImportSubscribersSkipsInvalidEmails(t *testing.T) {
	s := newTestStore(t)
	imported, skipped, invalid, err := s.ImportSubscribers([]string{
		"good@x.com", "not-an-email", "@no-local.com", "", "also@good.org",
	})
	if err != nil {
		t.Fatalf("ImportSubscribers: %v", err)
	}
	if imported != 2 || skipped != 0 || invalid != 3 {
		t.Errorf("imported=%d skipped=%d invalid=%d, want 2/0/3", imported, skipped, invalid)
	}
	// Invalid rows must not exist.
	for _, bad := range []string{"not-an-email", "@no-local.com"} {
		if _, err := s.GetSubscriberByEmail(bad); err == nil {
			t.Errorf("invalid email %q was imported", bad)
		}
	}
	if _, err := s.GetSubscriberByEmail("good@x.com"); err != nil {
		t.Errorf("valid email missing: %v", err)
	}
}

// A legacy-list import is one batch: every valid row lands, duplicates and
// invalid rows are counted, and each imported subscriber can unsubscribe
// (has an unsub token). Scale matters — the import runs as a single
// transaction, not one implicit commit per row.
func TestImportSubscribersBulk(t *testing.T) {
	s := newTestStore(t)
	var emails []string
	for i := 0; i < 300; i++ {
		emails = append(emails, "user"+string(rune('a'+i%26))+strconv.Itoa(i)+"@x.com")
	}
	emails = append(emails, "user0@example.com") // dup within batch? no — distinct; add real dups below
	emails = append(emails, emails[0], emails[1], "not-an-email", "")

	imported, skipped, invalid, err := s.ImportSubscribers(emails)
	if err != nil {
		t.Fatalf("ImportSubscribers: %v", err)
	}
	if imported != 301 || skipped != 2 || invalid != 2 {
		t.Errorf("imported=%d skipped=%d invalid=%d, want 301/2/2", imported, skipped, invalid)
	}
	subs, err := s.ListSubscribers("active", "", 0, 400)
	if err != nil {
		t.Fatalf("ListSubscribers: %v", err)
	}
	if len(subs) != 301 {
		t.Errorf("active rows = %d, want 301", len(subs))
	}
	for _, sub := range subs {
		if sub.UnsubToken == "" {
			t.Fatalf("%s: imported subscriber without unsub token", sub.Email)
		}
		if sub.ConfirmedAt == 0 {
			t.Fatalf("%s: imported subscriber without confirmed_at", sub.Email)
		}
	}
}
