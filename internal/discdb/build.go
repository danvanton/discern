package discdb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// TheDiscDB's files, as far as the index needs them. Field names are its
// own (data/movie/<Title (Year)>/metadata.json and <release>/discNN.json).
type (
	tddMetadata struct {
		Title       string
		Year        int
		ExternalIds struct {
			Tmdb string
			Imdb string
		}
	}
	tddDisc struct {
		Index       int
		Name        string
		Format      string
		ContentHash string
		Titles      []struct {
			Index      int
			SourceFile string
			SegmentMap string
			Duration   string // "1:56:31"
			Size       int64
			Item       *struct {
				Title string
				Type  string
			}
		}
	}
)

// BuildReport says what a build kept and what it skipped.
type BuildReport struct {
	Movies, Discs int
	Skipped       []string // files that couldn't be read, with why
}

// Build reads the movie discs of a TheDiscDB checkout (the repository's
// root, holding data/movie) into an index. A disc or movie file that doesn't
// parse is skipped and reported: one bad record in a community catalogue
// mustn't stop the index. No discs at all is an error.
func Build(root, source string, now time.Time) (*Index, BuildReport, error) {
	var rep BuildReport
	ix := &Index{Version: Version, Built: now.UTC(), Source: source}
	movies, err := os.ReadDir(filepath.Join(root, "data", "movie"))
	if err != nil {
		return nil, rep, err
	}
	for _, m := range movies {
		if !m.IsDir() {
			continue
		}
		dir := filepath.Join(root, "data", "movie", m.Name())
		movie, err := readMovie(dir)
		if err != nil {
			rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s: %v", m.Name(), err))
			continue
		}
		releases, err := os.ReadDir(dir)
		if err != nil {
			return nil, rep, err
		}
		found := false
		for _, r := range releases {
			if !r.IsDir() {
				continue
			}
			files, err := filepath.Glob(filepath.Join(dir, r.Name(), "disc*.json"))
			if err != nil {
				return nil, rep, err
			}
			slices.Sort(files)
			for _, f := range files {
				// TheDiscDB's newer optical-disc-manifest files (discNN.odm.json)
				// sit next to the discNN.json records, in another format.
				if strings.HasSuffix(f, ".odm.json") {
					continue
				}
				d, err := readDisc(f)
				if err != nil {
					rep.Skipped = append(rep.Skipped, fmt.Sprintf("%s/%s/%s: %v", m.Name(), r.Name(), filepath.Base(f), err))
					continue
				}
				d.Movie, d.Release = movie, r.Name()
				ix.Discs = append(ix.Discs, d)
				rep.Discs++
				found = true
			}
		}
		if found {
			rep.Movies++
		}
	}
	if err := ix.check(); err != nil {
		return nil, rep, err
	}
	ix.prepare()
	return ix, rep, nil
}

func readMovie(dir string) (Movie, error) {
	var md tddMetadata
	if err := readJSON(filepath.Join(dir, "metadata.json"), &md); err != nil {
		return Movie{}, err
	}
	id, err := strconv.Atoi(md.ExternalIds.Tmdb)
	if err != nil || id <= 0 {
		return Movie{}, fmt.Errorf("no TMDb id (%q)", md.ExternalIds.Tmdb)
	}
	return Movie{TMDbID: id, IMDbID: md.ExternalIds.Imdb, Title: md.Title, Year: md.Year}, nil
}

func readDisc(path string) (Disc, error) {
	var td tddDisc
	if err := readJSON(path, &td); err != nil {
		return Disc{}, err
	}
	if len(td.Titles) == 0 {
		return Disc{}, errors.New("no titles")
	}
	d := Disc{Name: td.Name, Format: td.Format, ContentHash: td.ContentHash}
	for _, t := range td.Titles {
		secs, err := seconds(t.Duration)
		if err != nil {
			return Disc{}, fmt.Errorf("title %d: %w", t.Index, err)
		}
		title := Title{Index: t.Index, File: t.SourceFile, Segments: t.SegmentMap, Seconds: secs, Size: t.Size}
		if t.Item != nil {
			title.Type, title.Name = t.Item.Type, strings.TrimSpace(t.Item.Title)
		}
		d.Titles = append(d.Titles, title)
	}
	return d, nil
}

// seconds reads TheDiscDB's "h:mm:ss" lengths.
func seconds(s string) (int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("length %q is not h:mm:ss", s)
	}
	total := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("length %q is not h:mm:ss", s)
		}
		total = total*60 + n
	}
	return total, nil
}

// readJSON reads one of TheDiscDB's files. Disc files run to a few MB with
// their track lists, so a much bigger one is refused rather than read.
func readJSON(path string, v any) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > 32<<20 {
		return fmt.Errorf("%d bytes, too big for a disc record", info.Size())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
