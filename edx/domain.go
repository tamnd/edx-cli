package edx

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/tamnd/any-cli/kit"
	"github.com/tamnd/any-cli/kit/errs"
)

// domain.go registers the edx kit Domain so a blank import in a multi-domain
// host enables the driver:
//
//	import _ "github.com/tamnd/edx-cli/edx"
func init() { kit.Register(Domain{}) }

// Domain is the edx.org driver.
type Domain struct{}

// Info describes the scheme and the identity the single-site binary inherits.
func (Domain) Info() kit.DomainInfo {
	return kit.DomainInfo{
		Scheme:  "edx",
		Aliases: []string{"edxorg"},
		Hosts:   []string{Host, "www." + Host},
		Identity: kit.Identity{
			Binary: "edx",
			Short:  "Search and browse courses on edX",
			Long: `edx is a command-line tool for edx.org, the MOOC platform from MIT and Harvard.
It reads course metadata from public pages using JSON-LD and HTML parsing.
No API key needed.

Note: edX's search page is JavaScript-rendered (Algolia). The search command
extracts any structured data available in the initial HTML. Use "edx course"
for reliable metadata on a specific course.

Quick start:
  edx subjects
  edx course machine-learning/stanford-university-machine-learning
  edx top --subject computer-science
  edx search "machine learning"`,
			Site: Host,
			Repo: "https://github.com/tamnd/edx-cli",
		},
	}
}

// Register installs the client factory and the four edx operations onto app.
func (Domain) Register(app *kit.App) {
	app.SetClient(newClient)

	kit.Handle(app, kit.OpMeta{
		Name:    "search",
		Group:   "search",
		Summary: "Search for courses by keyword (note: edX search is JavaScript-rendered)",
		Args:    []kit.Arg{{Name: "query", Help: "search keyword"}},
	}, searchOp)

	kit.Handle(app, kit.OpMeta{
		Name:     "course",
		Group:    "read",
		Single:   true,
		Resolver: true,
		URIType:  "course",
		Summary:  "Fetch details for one course from JSON-LD metadata",
		Args:     []kit.Arg{{Name: "slug", Help: "course slug, path, or URL"}},
	}, courseOp)

	kit.Handle(app, kit.OpMeta{
		Name:    "subjects",
		Group:   "browse",
		Summary: "List available subject areas (offline, no network needed)",
	}, subjectsOp)

	kit.Handle(app, kit.OpMeta{
		Name:    "top",
		Group:   "browse",
		Summary: "List featured or popular courses for a subject",
	}, topOp)
}

// newClient builds a Client from the kit Config.
func newClient(_ context.Context, cfg kit.Config) (any, error) {
	c := DefaultConfig()
	if cfg.Rate > 0 {
		c.Rate = cfg.Rate
	}
	if cfg.Retries > 0 {
		c.Retries = cfg.Retries
	}
	if cfg.Timeout > 0 {
		c.Timeout = cfg.Timeout
	}
	if cfg.UserAgent != "" {
		c.UserAgent = cfg.UserAgent
	}
	return NewClient(c), nil
}

// --- input structs ---

type searchInput struct {
	Query   string  `kit:"arg"          help:"search keyword"`
	Subject string  `kit:"flag"         help:"filter by subject slug (e.g. computer-science)"`
	Limit   int     `kit:"flag,inherit" help:"max results"    default:"20"`
	Client  *Client `kit:"inject"`
}

type courseInput struct {
	Slug   string  `kit:"arg"   help:"course slug, path, or URL"`
	Client *Client `kit:"inject"`
}

type subjectsInput struct {
	// no client needed: subjects is offline
}

type topInput struct {
	Subject string  `kit:"flag"         help:"subject slug (computer-science, data-science, ...)"`
	Limit   int     `kit:"flag,inherit" help:"max courses" default:"20"`
	Client  *Client `kit:"inject"`
}

// --- handlers ---

func searchOp(ctx context.Context, in searchInput, emit func(Course) error) error {
	courses, err := in.Client.Search(ctx, in.Query, in.Subject, in.Limit)
	if err != nil {
		return mapErr(err)
	}
	for _, c := range courses {
		if err := emit(c); err != nil {
			return err
		}
	}
	return nil
}

func courseOp(ctx context.Context, in courseInput, emit func(*Course) error) error {
	c, err := in.Client.GetCourse(ctx, in.Slug)
	if err != nil {
		return mapErr(err)
	}
	return emit(c)
}

func subjectsOp(_ context.Context, _ subjectsInput, emit func(Subject) error) error {
	for _, s := range Subjects() {
		if err := emit(s); err != nil {
			return err
		}
	}
	return nil
}

func topOp(ctx context.Context, in topInput, emit func(Course) error) error {
	courses, err := in.Client.Top(ctx, in.Subject, in.Limit)
	if err != nil {
		return mapErr(err)
	}
	if len(courses) == 0 {
		return errs.NoResults("no courses found")
	}
	for _, c := range courses {
		if err := emit(c); err != nil {
			return err
		}
	}
	return nil
}

// --- Resolver ---

// Classify turns a course slug or edX URL into the canonical (uriType, id).
func (Domain) Classify(input string) (uriType, id string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", errs.Usage("edx: empty input")
	}
	// Full URL.
	if u, err := url.Parse(input); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 3 && parts[0] == "learn" {
			return "course", parts[1] + "/" + parts[2], nil
		}
		return "", "", errs.Usage("edx: unrecognized edX URL: %q", input)
	}
	// Path with /learn/.
	input = strings.TrimPrefix(input, "/learn/")
	input = strings.TrimPrefix(input, "learn/")
	// Anything with at least one segment is a valid slug.
	if input != "" {
		return "course", input, nil
	}
	return "", "", errs.Usage("edx: unrecognized reference: %q", input)
}

// Locate returns the canonical edX URL for a (uriType, id).
func (Domain) Locate(uriType, id string) (string, error) {
	switch uriType {
	case "course":
		// id may be "subject/slug" or just "slug".
		if strings.Contains(id, "/") {
			return BaseURL + "/learn/" + id, nil
		}
		return BaseURL + "/learn/" + id, nil
	default:
		return "", errs.Usage("edx has no resource type %q", uriType)
	}
}

// mapErr converts library errors into kit error kinds with appropriate exit codes.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) {
		return errs.NotFound("%s", err.Error())
	}
	if errors.Is(err, ErrNoData) {
		return errs.NoResults("%s", err.Error())
	}
	if errors.Is(err, ErrBlocked) {
		return errs.RateLimited("%s", err.Error())
	}
	return err
}
