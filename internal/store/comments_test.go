package store

import (
	"testing"
	"time"
)

func TestCommentLifecycle(t *testing.T) {
	s := newTestStore(t)

	id, err := s.CreateComment("ep-01", "小明", "ming@x.com", "说得真好", "iphash", "ua-agent")
	if err != nil {
		t.Fatalf("CreateComment: %v", err)
	}
	c, err := s.GetComment(id)
	if err != nil {
		t.Fatalf("GetComment: %v", err)
	}
	if c.Status != "pending" {
		t.Errorf("status = %q, want pending", c.Status)
	}
	if c.PostSlug != "ep-01" || c.AuthorName != "小明" {
		t.Errorf("comment fields wrong: %+v", c)
	}

	// Pending comments are not listed publicly.
	pub, err := s.ListApprovedComments("ep-01")
	if err != nil {
		t.Fatalf("ListApprovedComments: %v", err)
	}
	if len(pub) != 0 {
		t.Errorf("pending comment visible: %+v", pub)
	}

	// Approve → visible, moderated_at set.
	if err := s.ModerateComment(id, "approved"); err != nil {
		t.Fatalf("ModerateComment approve: %v", err)
	}
	pub, _ = s.ListApprovedComments("ep-01")
	if len(pub) != 1 {
		t.Fatalf("approved comment not visible")
	}
	if pub[0].Content != "说得真好" {
		t.Errorf("content = %q", pub[0].Content)
	}

	// Reject → hidden again.
	if err := s.ModerateComment(id, "rejected"); err != nil {
		t.Fatalf("ModerateComment reject: %v", err)
	}
	pub, _ = s.ListApprovedComments("ep-01")
	if len(pub) != 0 {
		t.Errorf("rejected comment visible")
	}

	// Invalid moderation status rejected by store.
	if err := s.ModerateComment(id, "nonsense"); err == nil {
		t.Error("ModerateComment with invalid status must fail")
	}
}

func TestApprovedCommentsOrderingAndIsolation(t *testing.T) {
	s := newTestStore(t)
	for i, body := range []string{"first", "second", "third"} {
		id, err := s.CreateComment("ep-01", "n", "e", body, "", "")
		if err != nil {
			t.Fatalf("CreateComment %d: %v", i, err)
		}
		if err := s.ModerateComment(id, "approved"); err != nil {
			t.Fatalf("ModerateComment: %v", err)
		}
	}
	// Other post's comment must not leak.
	other, _ := s.CreateComment("ep-02", "n", "e", "other", "", "")
	_ = s.ModerateComment(other, "approved")

	list, err := s.ListApprovedComments("ep-01")
	if err != nil {
		t.Fatalf("ListApprovedComments: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	if list[0].Content != "first" || list[2].Content != "third" {
		t.Errorf("not chronological: %+v", list)
	}
	// Public projection must not expose author_email.
	if list[0].AuthorEmail != "" {
		t.Errorf("author_email leaked in public list")
	}
}

func TestListCommentsForAdmin(t *testing.T) {
	s := newTestStore(t)
	id1, _ := s.CreateComment("a", "n1", "e", "c1", "", "")
	id2, _ := s.CreateComment("a", "n2", "e", "c2", "", "")
	_ = id2
	pending, err := s.ListComments("pending")
	if err != nil {
		t.Fatalf("ListComments: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %d, want 2", len(pending))
	}
	if err := s.ModerateComment(id1, "approved"); err != nil {
		t.Fatalf("ModerateComment: %v", err)
	}
	pending, _ = s.ListComments("pending")
	if len(pending) != 1 {
		t.Errorf("pending after approve = %d, want 1", len(pending))
	}
	n, err := s.CountComments("pending")
	if err != nil {
		t.Fatalf("CountComments: %v", err)
	}
	if n != 1 {
		t.Errorf("CountComments pending = %d, want 1", n)
	}
}

func TestCommentTimestampsUTC(t *testing.T) {
	s := newTestStore(t)
	before := time.Now().Unix()
	id, _ := s.CreateComment("a", "n", "e", "c", "", "")
	c, _ := s.GetComment(id)
	if c.CreatedAt < before || c.CreatedAt > time.Now().Unix() {
		t.Errorf("created_at %d out of range", c.CreatedAt)
	}
	if err := s.ModerateComment(id, "approved"); err != nil {
		t.Fatalf("ModerateComment: %v", err)
	}
	c, _ = s.GetComment(id)
	if c.ModeratedAt < before {
		t.Errorf("moderated_at not set: %d", c.ModeratedAt)
	}
}
