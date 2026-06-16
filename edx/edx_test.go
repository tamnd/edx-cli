package edx_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tamnd/edx-cli/edx"
)

func TestDefaultConfig(t *testing.T) {
	cfg := edx.DefaultConfig()
	if cfg.Rate <= 0 {
		t.Errorf("Rate = %v, want > 0", cfg.Rate)
	}
	if cfg.Retries <= 0 {
		t.Errorf("Retries = %d, want > 0", cfg.Retries)
	}
	if cfg.Timeout <= 0 {
		t.Errorf("Timeout = %v, want > 0", cfg.Timeout)
	}
	if cfg.UserAgent == "" {
		t.Error("UserAgent is empty")
	}
}

func TestNewClientNotNil(t *testing.T) {
	c := edx.NewClient(edx.DefaultConfig())
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
}

func TestCourseRoundTrip(t *testing.T) {
	want := edx.Course{
		Slug:    "stanford-machine-learning",
		Title:   "Machine Learning",
		Org:     "Stanford University",
		Subject: "machine-learning",
		URL:     "https://www.edx.org/learn/machine-learning/stanford-machine-learning",
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got edx.Course
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Slug != want.Slug || got.Title != want.Title || got.Org != want.Org {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestSubjectRoundTrip(t *testing.T) {
	want := edx.Subject{
		Slug: "computer-science",
		Name: "Computer Science",
		URL:  "https://www.edx.org/learn/computer-science",
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got edx.Subject
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Slug != want.Slug || got.Name != want.Name {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

const courseJSONLD = `<!DOCTYPE html>
<html>
<head>
<title>Machine Learning | edX</title>
<meta property="og:title" content="Machine Learning" />
<meta property="og:description" content="Learn ML from Stanford." />
<meta property="og:url" content="https://www.edx.org/learn/machine-learning/stanford-ml" />
<script type="application/ld+json">
{
  "@context": "http://schema.org",
  "@type": "Course",
  "name": "Machine Learning",
  "description": "Learn about the most effective machine learning techniques.",
  "url": "https://www.edx.org/learn/machine-learning/stanford-ml",
  "provider": {
    "@type": "Organization",
    "name": "Stanford University"
  },
  "inLanguage": "en",
  "educationalLevel": "Introductory"
}
</script>
</head>
<body><p>Course content here.</p></body>
</html>`

func TestClientCourseFromTestServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(courseJSONLD))
	}))
	defer srv.Close()

	cfg := edx.DefaultConfig()
	cfg.Rate = 0
	cfg.BaseURL = srv.URL
	c := edx.NewClient(cfg)

	course, err := c.GetCourse(context.Background(), srv.URL+"/learn/machine-learning/stanford-ml")
	if err != nil {
		t.Fatalf("GetCourse: %v", err)
	}
	if course.Title != "Machine Learning" {
		t.Errorf("Title = %q, want Machine Learning", course.Title)
	}
	if course.Org != "Stanford University" {
		t.Errorf("Org = %q, want Stanford University", course.Org)
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := edx.DefaultConfig()
	cfg.Rate = 0
	cfg.Retries = 0
	c := edx.NewClient(cfg)

	// Cancelled context should cause an error, not panic.
	_, err := c.GetCourse(ctx, "machine-learning/stanford-ml")
	if err == nil {
		t.Error("expected error with cancelled context, got nil")
	}
}

func TestGetRetriesOn503(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>ok</body></html>"))
	}))
	defer srv.Close()

	cfg := edx.DefaultConfig()
	cfg.Rate = 0
	cfg.Retries = 5
	cfg.BaseURL = srv.URL
	c := edx.NewClient(cfg)

	start := time.Now()
	_, _ = c.GetCourse(context.Background(), srv.URL+"/learn/x/y")
	if hits < 3 {
		t.Errorf("server saw %d hits, want >= 3", hits)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Error("retries did not back off")
	}
}

func TestHostConstant(t *testing.T) {
	if edx.Host != "edx.org" {
		t.Errorf("Host = %q, want edx.org", edx.Host)
	}
}

func TestErrSentinels(t *testing.T) {
	if edx.ErrNotFound == nil {
		t.Error("ErrNotFound is nil")
	}
	if edx.ErrNoData == nil {
		t.Error("ErrNoData is nil")
	}
	if edx.ErrBlocked == nil {
		t.Error("ErrBlocked is nil")
	}
}

func TestSubjects(t *testing.T) {
	subjects := edx.Subjects()
	if len(subjects) == 0 {
		t.Fatal("Subjects() returned empty list")
	}
	for _, s := range subjects {
		if s.Slug == "" {
			t.Error("subject has empty slug")
		}
		if s.Name == "" {
			t.Error("subject has empty name")
		}
		if s.URL == "" {
			t.Error("subject has empty URL")
		}
	}
}

func TestParseJSONLD(t *testing.T) {
	course, err := edx.ParseCourseJSONLD([]byte(courseJSONLD))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if course.Title != "Machine Learning" {
		t.Errorf("Title = %q, want Machine Learning", course.Title)
	}
	if course.Org != "Stanford University" {
		t.Errorf("Org = %q, want Stanford University", course.Org)
	}
}

func TestParseSearchEmpty(t *testing.T) {
	courses, err := edx.ParseSearchPage([]byte("<html><body></body></html>"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(courses) != 0 {
		t.Errorf("expected 0 courses, got %d", len(courses))
	}
}
