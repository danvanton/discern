// Package radarr is a small client for the parts of Radarr's v3 API that
// Trayarr uses: movie lookup, adding a movie, Manual Import and commands.
// It knows Radarr's API and nothing about discs or jobs.
package radarr

import (
	"bytes"
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
)

// Client talks to one Radarr instance.
type Client struct {
	base   *url.URL
	apiKey string
	http   *http.Client
}

// New returns a client for the Radarr at baseURL (for example
// "http://radarr:7878"), authenticating with apiKey.
func New(baseURL, apiKey string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("radarr URL %q must look like http://host:7878", baseURL)
	}
	if apiKey == "" {
		return nil, errors.New("radarr API key is empty")
	}
	// Every request is bounded; Radarr answers these in milliseconds.
	return &Client{base: u, apiKey: apiKey, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

// APIError is a non-2xx answer from Radarr. Body holds the start of what
// Radarr said, which usually names the problem.
type APIError struct {
	Method string
	Path   string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("radarr %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// do sends one request. query may be nil; body, if not nil, is sent as JSON;
// out, if not nil, receives the decoded answer. The API key goes in a header,
// never in the URL, so it can't end up in a log line.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := *c.base
	u.Path = c.base.Path + "/api/v3" + path
	u.RawQuery = query.Encode()

	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("radarr %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return &APIError{Method: method, Path: path, Status: resp.StatusCode, Body: strings.TrimSpace(string(msg))}
	}
	if out == nil {
		return nil
	}
	// An empty body (204 No Content, or an empty 200) leaves out at its zero
	// value. Callers that need an answer check for that and say so.
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("radarr %s %s: reading the answer: %w", method, path, err)
	}
	return nil
}

// Movie is the subset of Radarr's movie resource Trayarr reads.
type Movie struct {
	ID        int        `json:"id"`
	Title     string     `json:"title"`
	Year      int        `json:"year"`
	TMDbID    int        `json:"tmdbId"`
	HasFile   bool       `json:"hasFile"`
	Path      string     `json:"path"`
	MovieFile *MovieFile `json:"movieFile,omitempty"`
}

// MovieFile is the movie's file, once it has one.
type MovieFile struct {
	RelativePath string    `json:"relativePath"`
	Quality      Quality   `json:"quality"`
	DateAdded    time.Time `json:"dateAdded"`
}

// Quality is Radarr's quality-with-revision object, as read back from a
// movie file. Only the name is used. What Trayarr sends is the raw object
// from QualityNamed.
type Quality struct {
	Quality QualityInfo `json:"quality"`
}

// QualityInfo names one quality, such as "Remux-2160p".
type QualityInfo struct {
	Name string `json:"name"`
}

// Lookup searches TMDb through Radarr. Results carry TMDbID, Title and Year;
// ID is 0 for movies not in the library.
func (c *Client) Lookup(ctx context.Context, term string) ([]Movie, error) {
	var out []Movie
	err := c.do(ctx, http.MethodGet, "/movie/lookup", url.Values{"term": {term}}, nil, &out)
	return out, err
}

// LookupTMDb looks one movie up on TMDb by its id, for when the user types
// the id to correct an identification.
func (c *Client) LookupTMDb(ctx context.Context, tmdbID int) (*Movie, error) {
	var m Movie
	if err := c.do(ctx, http.MethodGet, "/movie/lookup/tmdb", url.Values{"tmdbId": {strconv.Itoa(tmdbID)}}, nil, &m); err != nil {
		return nil, err
	}
	if m.TMDbID != tmdbID {
		return nil, fmt.Errorf("TMDb has no movie %d", tmdbID)
	}
	return &m, nil
}

// SystemStatus is the part of Radarr's status Trayarr shows.
type SystemStatus struct {
	Version string `json:"version"`
}

// SystemStatus checks that Radarr answers and the key works; the health
// page uses it.
func (c *Client) SystemStatus(ctx context.Context) (*SystemStatus, error) {
	var s SystemStatus
	if err := c.do(ctx, http.MethodGet, "/system/status", nil, nil, &s); err != nil {
		return nil, err
	}
	if s.Version == "" {
		return nil, errors.New("radarr answered without a version")
	}
	return &s, nil
}

// MovieByTMDb returns the library's movie with this TMDb id, or nil if the
// library doesn't have it. It filters the answer itself, so it is correct
// even if a Radarr version ignores the tmdbId parameter and lists every movie.
func (c *Client) MovieByTMDb(ctx context.Context, tmdbID int) (*Movie, error) {
	var all []Movie
	if err := c.do(ctx, http.MethodGet, "/movie", url.Values{"tmdbId": {strconv.Itoa(tmdbID)}}, nil, &all); err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].TMDbID == tmdbID {
			return &all[i], nil
		}
	}
	return nil, nil
}

// Movie returns one movie by its Radarr id.
func (c *Client) Movie(ctx context.Context, id int) (*Movie, error) {
	var m Movie
	if err := c.do(ctx, http.MethodGet, "/movie/"+strconv.Itoa(id), nil, nil, &m); err != nil {
		return nil, err
	}
	if m.ID == 0 {
		return nil, fmt.Errorf("radarr returned no movie for id %d", id)
	}
	return &m, nil
}

// NewMovie is what AddMovie sends.
type NewMovie struct {
	TMDbID           int    `json:"tmdbId"`
	Title            string `json:"title"`
	Year             int    `json:"year"`
	QualityProfileID int    `json:"qualityProfileId"`
	RootFolderPath   string `json:"rootFolderPath"`
	// There is deliberately no Monitored field: AddMovie always adds the
	// movie unmonitored and without a search, so Trayarr never makes Radarr
	// download anything.
}

// AddMovie adds a movie to the library, unmonitored and without searching
// for it.
func (c *Client) AddMovie(ctx context.Context, m NewMovie) (*Movie, error) {
	body := map[string]any{
		"tmdbId":           m.TMDbID,
		"title":            m.Title,
		"year":             m.Year,
		"qualityProfileId": m.QualityProfileID,
		"rootFolderPath":   m.RootFolderPath,
		"monitored":        false,
		"addOptions":       map[string]any{"searchForMovie": false, "monitor": "none"},
	}
	var out Movie
	if err := c.do(ctx, http.MethodPost, "/movie", nil, body, &out); err != nil {
		return nil, err
	}
	if out.ID == 0 {
		return nil, fmt.Errorf("radarr added %s but returned no movie id", m.Title)
	}
	return &out, nil
}

// QualityProfile is a named quality profile.
type QualityProfile struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (c *Client) QualityProfiles(ctx context.Context) ([]QualityProfile, error) {
	var out []QualityProfile
	err := c.do(ctx, http.MethodGet, "/qualityprofile", nil, nil, &out)
	return out, err
}

// QualityDefinition pairs a quality with its limits. Only the quality is
// used.
type QualityDefinition struct {
	Quality json.RawMessage `json:"quality"`
}

// QualityNamed returns Radarr's own quality object for name, ready to send
// in a Manual Import. It is returned raw so every field Radarr has goes back
// to it unchanged.
func (c *Client) QualityNamed(ctx context.Context, name string) (json.RawMessage, error) {
	var defs []QualityDefinition
	if err := c.do(ctx, http.MethodGet, "/qualitydefinition", nil, nil, &defs); err != nil {
		return nil, err
	}
	var names []string
	for _, d := range defs {
		var q QualityInfo
		if json.Unmarshal(d.Quality, &q) == nil {
			if q.Name == name {
				return d.Quality, nil
			}
			names = append(names, q.Name)
		}
	}
	return nil, fmt.Errorf("radarr has no quality named %q (it has: %s)", name, strings.Join(names, ", "))
}

// ManualImportScan asks Radarr to scan folder (a path as Radarr sees it) and
// returns its entries as raw objects. Trayarr sends an entry back with the
// movie and quality set, so it keeps every field Radarr filled in.
func (c *Client) ManualImportScan(ctx context.Context, folder string) ([]map[string]any, error) {
	var out []map[string]any
	err := c.do(ctx, http.MethodGet, "/manualimport",
		url.Values{"folder": {folder}, "filterExistingFiles": {"false"}}, nil, &out)
	return out, err
}

// Command is Radarr's command resource.
type Command struct {
	ID        int             `json:"id"`
	Name      string          `json:"name"`
	Status    string          `json:"status"` // queued, started, completed, failed, aborted, cancelled, orphaned
	Message   string          `json:"message"`
	Exception string          `json:"exception"`
	Body      json.RawMessage `json:"body"` // what the command was asked to do
}

// Commands lists Radarr's current and recent commands.
func (c *Client) Commands(ctx context.Context) ([]Command, error) {
	var out []Command
	err := c.do(ctx, http.MethodGet, "/command", nil, nil, &out)
	return out, err
}

// ImportsPath reports whether a ManualImport command was asked to import
// remotePath.
func (c Command) ImportsPath(remotePath string) bool {
	if c.Name != "ManualImport" {
		return false
	}
	var body struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if json.Unmarshal(c.Body, &body) != nil {
		return false
	}
	for _, f := range body.Files {
		if f.Path == remotePath {
			return true
		}
	}
	return false
}

// Done reports whether the command has finished, successfully or not.
func (c Command) Done() bool {
	switch c.Status {
	case "completed", "failed", "aborted", "cancelled", "orphaned":
		return true
	}
	return false
}

// StartManualImport queues a Manual Import that moves the files.
func (c *Client) StartManualImport(ctx context.Context, files []map[string]any) (*Command, error) {
	body := map[string]any{"name": "ManualImport", "importMode": "move", "files": files}
	var out Command
	if err := c.do(ctx, http.MethodPost, "/command", nil, body, &out); err != nil {
		return nil, err
	}
	if out.ID == 0 {
		return nil, errors.New("radarr queued the import but returned no command id, so it can't be followed")
	}
	return &out, nil
}

// Command returns the current state of one command.
func (c *Client) Command(ctx context.Context, id int) (*Command, error) {
	var out Command
	if err := c.do(ctx, http.MethodGet, "/command/"+strconv.Itoa(id), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// maxPollErrors is how many status requests in a row may fail before
// WaitForCommand gives up. One failure (Radarr restarting, a dropped
// connection) doesn't mean the command failed: it keeps running in Radarr.
const maxPollErrors = 5

// WaitForCommand polls this command (not Radarr's whole queue) until it
// finishes or ctx ends.
func (c *Client) WaitForCommand(ctx context.Context, id int, every time.Duration) (*Command, error) {
	t := time.NewTicker(every)
	defer t.Stop()
	status := "unknown"
	failures := 0
	for {
		cmd, err := c.Command(ctx, id)
		switch {
		case err != nil && ctx.Err() == nil:
			failures++
			if failures >= maxPollErrors {
				return nil, fmt.Errorf("radarr command %d: %d status checks in a row failed, last: %w", id, failures, err)
			}
		case err == nil:
			failures = 0
			status = cmd.Status
			if cmd.Done() {
				return cmd, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("radarr command %d still %s: %w", id, status, ctx.Err())
		case <-t.C:
		}
	}
}
