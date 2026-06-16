// Package edx is the library behind the edx command: the HTTP client,
// pacing, and the typed data models for edx.org.
//
// edX (edx.org) is a large MOOC platform offering courses from MIT, Harvard,
// Google, and hundreds of other institutions.
//
// Architecture note: edX's search page is JavaScript-rendered (React/Next.js +
// Algolia). The search command parses any JSON-LD or structured data present in
// the initial HTML. The course command always works because course pages embed
// JSON-LD metadata in the initial HTML. The subjects command is offline.
package edx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// Host is the site this client talks to.
	Host = "edx.org"

	// BaseURL is the root every request is built from.
	BaseURL = "https://www." + Host

	// DefaultUserAgent is a realistic browser string.
	DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

// Sentinel errors returned by Client methods.
var (
	// ErrNotFound is returned when a course does not exist (404).
	ErrNotFound = errors.New("not found")

	// ErrNoData is returned when the page loaded but contained no parseable
	// course data (e.g. a client-side rendered search page).
	ErrNoData = errors.New("no course data found; edX search is JavaScript-rendered. " +
		"Try 'edx course <slug>' for a specific course or 'edx top' for featured courses")

	// ErrBlocked is returned when the site blocks the request.
	ErrBlocked = errors.New("blocked or rate limited by site")
)

// Course holds metadata for one edX course.
type Course struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Org         string `json:"org,omitempty"`
	Subject     string `json:"subject,omitempty"`
	Description string `json:"description,omitempty"`
	Level       string `json:"level,omitempty"`
	Effort      string `json:"effort,omitempty"`
	Language    string `json:"language,omitempty"`
	Price       string `json:"price,omitempty"`
	Certificate bool   `json:"certificate,omitempty"`
	URL         string `json:"url"`
}

// Subject holds one top-level subject area from edX.
type Subject struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Config holds constructor parameters for Client.
type Config struct {
	BaseURL   string
	UserAgent string
	Rate      time.Duration
	Retries   int
	Timeout   time.Duration
}

// DefaultConfig returns sensible defaults for edx.org.
func DefaultConfig() Config {
	return Config{
		BaseURL:   BaseURL,
		UserAgent: DefaultUserAgent,
		Rate:      300 * time.Millisecond,
		Retries:   3,
		Timeout:   30 * time.Second,
	}
}

// Client is a rate-limited HTTP client for edx.org.
type Client struct {
	cfg  Config
	http *http.Client
	mu   sync.Mutex
	last time.Time
}

// NewClient returns a Client configured with cfg.
func NewClient(cfg Config) *Client {
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
	}
}

// GetCourse fetches and parses the JSON-LD metadata from an edX course page.
func (c *Client) GetCourse(ctx context.Context, slugOrURL string) (*Course, error) {
	subject, slug := extractCourseSlug(slugOrURL)
	var pageURL string
	if subject != "" {
		pageURL = c.cfg.BaseURL + "/learn/" + subject + "/" + slug
	} else if strings.HasPrefix(slugOrURL, "http") {
		pageURL = slugOrURL
	} else {
		// Try to find the course page by searching for it.
		// Use the slug as a best-effort path guess.
		pageURL = c.cfg.BaseURL + "/learn/" + slug
	}

	body, err := c.get(ctx, pageURL)
	if err != nil {
		return nil, err
	}

	course, err := ParseCourseJSONLD(body)
	if err != nil {
		return nil, fmt.Errorf("parse course page: %w", err)
	}
	if course.Title == "" {
		return nil, ErrNotFound
	}
	if course.Slug == "" {
		course.Slug = slug
	}
	if course.URL == "" {
		course.URL = pageURL
	}
	return &course, nil
}

// Search fetches the edX search page and extracts any available course data.
// Because edX's search page is JavaScript-rendered, this may return ErrNoData.
func (c *Client) Search(ctx context.Context, query, subject string, limit int) ([]Course, error) {
	vals := url.Values{"q": {query}}
	if subject != "" {
		vals.Set("subject", subject)
	}
	u := c.cfg.BaseURL + "/search?" + vals.Encode()
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	courses, err := ParseSearchPage(body)
	if err != nil {
		return nil, err
	}
	if len(courses) == 0 {
		return nil, ErrNoData
	}
	if limit > 0 && len(courses) > limit {
		courses = courses[:limit]
	}
	return courses, nil
}

// Top fetches the edX subject page and extracts course links.
func (c *Client) Top(ctx context.Context, subject string, limit int) ([]Course, error) {
	var u string
	if subject != "" {
		u = c.cfg.BaseURL + "/learn/" + subject
	} else {
		u = c.cfg.BaseURL
	}
	body, err := c.get(ctx, u)
	if err != nil {
		return nil, err
	}
	courses, err := ParseSearchPage(body)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(courses) > limit {
		courses = courses[:limit]
	}
	return courses, nil
}

// get fetches a URL with retries and pacing.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= c.cfg.Retries; attempt++ {
		if attempt > 0 {
			wait := time.Duration(attempt) * 500 * time.Millisecond
			if wait > 5*time.Second {
				wait = 5 * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		body, retry, err := c.do(ctx, rawURL)
		if err == nil {
			return body, nil
		}
		lastErr = err
		if !retry {
			return nil, err
		}
	}
	return nil, fmt.Errorf("get %s: %w", rawURL, lastErr)
}

func (c *Client) do(ctx context.Context, rawURL string) ([]byte, bool, error) {
	c.pace()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer func() { _ = resp.Body.Close() }()

	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, true, err
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, false, ErrNotFound
	}
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable {
		return nil, true, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("http %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("http %d", resp.StatusCode)
	}
	return b, false, nil
}

// pace enforces the inter-request delay.
func (c *Client) pace() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cfg.Rate > 0 {
		since := time.Since(c.last)
		if since < c.cfg.Rate {
			time.Sleep(c.cfg.Rate - since)
		}
	}
	c.last = time.Now()
}

// ParseCourseJSONLD finds and parses the JSON-LD Course object from an edX
// course page HTML body.
func ParseCourseJSONLD(body []byte) (Course, error) {
	// Find all <script type="application/ld+json">...</script> blocks.
	re := regexp.MustCompile(`(?s)<script[^>]*type="application/ld\+json"[^>]*>(.*?)</script>`)
	matches := re.FindAllSubmatch(body, -1)

	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		jsonData := bytes.TrimSpace(m[1])
		if len(jsonData) == 0 {
			continue
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(jsonData, &raw); err != nil {
			continue
		}

		typeVal := ""
		if t, ok := raw["@type"]; ok {
			_ = json.Unmarshal(t, &typeVal)
		}
		if typeVal != "Course" {
			// Could be an array of types or a different schema.
			var types []string
			if err := json.Unmarshal(raw["@type"], &types); err == nil {
				found := false
				for _, t := range types {
					if t == "Course" {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			} else {
				continue
			}
		}

		return parseJSONLDCourse(raw, string(body))
	}

	// Fallback: extract from og: meta tags.
	course := extractFromMeta(body)
	return course, nil
}

// jsonLDProvider is used to decode the Schema.org provider field.
type jsonLDProvider struct {
	Name string `json:"name"`
}

// parseJSONLDCourse converts a raw JSON-LD Course object to a Course struct.
func parseJSONLDCourse(raw map[string]json.RawMessage, htmlBody string) (Course, error) {
	c := Course{}

	if v, ok := raw["name"]; ok {
		_ = json.Unmarshal(v, &c.Title)
	}
	if v, ok := raw["description"]; ok {
		_ = json.Unmarshal(v, &c.Description)
	}
	if v, ok := raw["url"]; ok {
		_ = json.Unmarshal(v, &c.URL)
	}
	if v, ok := raw["provider"]; ok {
		var p jsonLDProvider
		if err := json.Unmarshal(v, &p); err == nil {
			c.Org = p.Name
		}
	}
	if v, ok := raw["inLanguage"]; ok {
		_ = json.Unmarshal(v, &c.Language)
	}
	if v, ok := raw["educationalLevel"]; ok {
		_ = json.Unmarshal(v, &c.Level)
	}
	if v, ok := raw["timeRequired"]; ok {
		var tr string
		if err := json.Unmarshal(v, &tr); err == nil {
			c.Effort = tr
		}
	}

	// Extract subject and slug from URL.
	if c.URL != "" {
		c.Subject, c.Slug = extractCourseSlug(c.URL)
	}

	// Fill missing fields from og: meta tags.
	meta := extractFromMeta([]byte(htmlBody))
	if c.Title == "" {
		c.Title = meta.Title
	}
	if c.Description == "" {
		c.Description = meta.Description
	}

	return c, nil
}

// extractFromMeta extracts course metadata from og: meta tags.
func extractFromMeta(body []byte) Course {
	c := Course{}
	s := string(body)

	if v := extractMetaTag(s, `property="og:title"`); v != "" {
		c.Title = v
	}
	if v := extractMetaTag(s, `property="og:description"`); v != "" {
		c.Description = v
	}
	if v := extractMetaTag(s, `property="og:url"`); v != "" {
		c.URL = v
		c.Subject, c.Slug = extractCourseSlug(v)
	}
	return c
}

// extractMetaTag extracts the content attribute of a meta tag matching selector.
func extractMetaTag(html, selector string) string {
	idx := strings.Index(html, selector)
	if idx == -1 {
		return ""
	}
	rest := html[idx:]
	cidx := strings.Index(rest, `content="`)
	if cidx == -1 {
		return ""
	}
	rest = rest[cidx+9:]
	end := strings.Index(rest, `"`)
	if end == -1 {
		return ""
	}
	return rest[:end]
}

// ParseSearchPage parses an edX search or subject listing page and returns
// any courses found in the initial HTML (JSON-LD or structured data).
func ParseSearchPage(body []byte) ([]Course, error) {
	s := string(body)
	var courses []Course
	seen := map[string]bool{}

	// Look for JSON-LD ItemList which contains courses.
	re := regexp.MustCompile(`(?s)<script[^>]*type="application/ld\+json"[^>]*>(.*?)</script>`)
	matches := re.FindAllSubmatch(body, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(bytes.TrimSpace(m[1]), &raw); err != nil {
			continue
		}
		typeVal := ""
		if t, ok := raw["@type"]; ok {
			_ = json.Unmarshal(t, &typeVal)
		}
		if typeVal == "Course" {
			c, err := parseJSONLDCourse(raw, s)
			if err == nil && c.Title != "" && !seen[c.URL] {
				seen[c.URL] = true
				courses = append(courses, c)
			}
		}
	}

	// Look for course links in the HTML (href="/learn/<subject>/<slug>").
	linkRE := regexp.MustCompile(`href="/learn/([^/"]+)/([^/"]+)"`)
	for _, m := range linkRE.FindAllStringSubmatch(s, -1) {
		if len(m) < 3 {
			continue
		}
		slug := m[1] + "/" + m[2]
		courseURL := BaseURL + "/learn/" + slug
		if seen[courseURL] {
			continue
		}
		seen[courseURL] = true
		courses = append(courses, Course{
			Slug:    m[2],
			Subject: m[1],
			URL:     courseURL,
		})
	}

	return courses, nil
}

// extractCourseSlug extracts (subject, slug) from an edX course URL or path.
// Examples:
//
//	"https://www.edx.org/learn/machine-learning/stanford-ml" -> ("machine-learning", "stanford-ml")
//	"machine-learning/stanford-ml" -> ("machine-learning", "stanford-ml")
//	"stanford-ml" -> ("", "stanford-ml")
func extractCourseSlug(input string) (subject, slug string) {
	input = strings.TrimSpace(input)
	if u, err := url.Parse(input); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		// Path: /learn/<subject>/<slug>
		for i, p := range parts {
			if p == "learn" && i+2 < len(parts) {
				return parts[i+1], parts[i+2]
			}
		}
		return "", ""
	}
	// Strip leading "/learn/" if present.
	input = strings.TrimPrefix(input, "/learn/")
	input = strings.TrimPrefix(input, "learn/")
	parts := strings.SplitN(input, "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", input
}

// Subjects returns the hardcoded list of edX subject areas.
func Subjects() []Subject {
	base := BaseURL + "/learn/"
	return []Subject{
		{Slug: "computer-science", Name: "Computer Science", URL: base + "computer-science"},
		{Slug: "data-science", Name: "Data Science", URL: base + "data-science"},
		{Slug: "business-management", Name: "Business & Management", URL: base + "business-management"},
		{Slug: "language", Name: "Language", URL: base + "language"},
		{Slug: "engineering", Name: "Engineering", URL: base + "engineering"},
		{Slug: "math-statistics", Name: "Math & Statistics", URL: base + "math-statistics"},
		{Slug: "science", Name: "Science", URL: base + "science"},
		{Slug: "social-sciences", Name: "Social Sciences", URL: base + "social-sciences"},
		{Slug: "humanities", Name: "Humanities", URL: base + "humanities"},
		{Slug: "art-design", Name: "Art & Design", URL: base + "art-design"},
		{Slug: "health-safety", Name: "Health & Safety", URL: base + "health-safety"},
		{Slug: "education-teacher", Name: "Education & Teacher Training", URL: base + "education-teacher-training"},
		{Slug: "philosophy-ethics", Name: "Philosophy & Ethics", URL: base + "philosophy-ethics"},
		{Slug: "communication", Name: "Communication", URL: base + "communication"},
		{Slug: "architecture", Name: "Architecture", URL: base + "architecture"},
		{Slug: "music", Name: "Music", URL: base + "music"},
		{Slug: "finance", Name: "Finance", URL: base + "finance"},
		{Slug: "law", Name: "Law", URL: base + "law"},
		{Slug: "food-nutrition", Name: "Food & Nutrition", URL: base + "food-nutrition"},
		{Slug: "history", Name: "History", URL: base + "history"},
	}
}
