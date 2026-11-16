// Package discdb holds TheDiscDB's movie discs in the compact form the
// selector needs (decision D38), and matches a scanned disc against them by
// shape (decision D39).
//
// TheDiscDB (https://github.com/TheDiscDb/data, MIT licence) lists every
// title of a known disc release, as MakeMKV shows it, and says which one is
// the movie. Its repository is about 1.6 GB, mostly images; the index keeps
// a few fields per title and is a few MB.
package discdb

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
	"unicode/utf8"
)

// Version is the index format this code reads and writes.
const Version = 1

// Index is TheDiscDB's movie discs, reduced to what the selector needs.
type Index struct {
	Version int       `json:"version"`
	Built   time.Time `json:"built"`
	Source  string    `json:"source"` // TheDiscDB's commit the index was built from
	Discs   []Disc    `json:"discs"`

	// Built by prepare, for Match.
	byShape  map[shape][]ref
	shortest []int // each disc's shortest title with a shape, in seconds; -1 if none
	shaped   []int // each disc's titles with a shape
}

// Disc is one disc of one release.
type Disc struct {
	Movie       Movie   `json:"movie"`
	Release     string  `json:"release"` // the release's folder, e.g. "2021-original-4-movie-collection-4k"
	Name        string  `json:"name"`    // TheDiscDB's name for the disc, often the movie's
	Format      string  `json:"format"`  // e.g. "UHD", "Blu-Ray"
	ContentHash string  `json:"content_hash,omitempty"`
	Titles      []Title `json:"titles"`
}

// Movie is the film a disc holds.
type Movie struct {
	TMDbID int    `json:"tmdb"`
	IMDbID string `json:"imdb,omitempty"`
	Title  string `json:"title"`
	Year   int    `json:"year"`
}

// Title is one title of a disc, as MakeMKV listed it when the disc was
// added to TheDiscDB.
type Title struct {
	Index    int    `json:"index"`
	File     string `json:"file"`     // the playlist or clip, e.g. "00800.mpls"
	Segments string `json:"segments"` // MakeMKV's segment map, e.g. "3,13,15,1"
	Seconds  int    `json:"seconds"`
	Size     int64  `json:"size"`
	Type     string `json:"type,omitempty"` // TheDiscDB's class: "MainMovie", "Extra", "Trailer"...
	Name     string `json:"name,omitempty"` // what it is, e.g. "Alien Director's Cut"
}

// MainMovie is TheDiscDB's class for the film itself. A disc with two cuts
// can have two.
const MainMovie = "MainMovie"

// Limits on a loaded index. It is downloaded, so it is checked like any
// untrusted input before use; the limits are far above TheDiscDB's size
// (about 3,000 movie discs, at most about 530 titles on one).
const (
	maxIndexBytes   = 256 << 20 // decompressed
	maxDiscs        = 100_000
	maxTitlesInDisc = 5_000
	maxText         = 300 // characters in any name
)

// Load reads an index written by Write, checks it, and prepares it for
// matching. A file that fails any check is an error: the caller keeps the
// last good index rather than use a doubtful one.
func Load(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f)
}

// Read is Load from a reader of the compressed index.
func Read(r io.Reader) (*Index, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("the disc index isn't gzip: %w", err)
	}
	defer zr.Close()
	lr := &io.LimitedReader{R: zr, N: maxIndexBytes + 1}
	var ix Index
	if err := json.NewDecoder(lr).Decode(&ix); err != nil {
		return nil, fmt.Errorf("the disc index doesn't parse: %w", err)
	}
	if lr.N <= 0 {
		return nil, fmt.Errorf("the disc index is over %d MB", maxIndexBytes>>20)
	}
	if err := ix.check(); err != nil {
		return nil, fmt.Errorf("the disc index is wrong: %w", err)
	}
	ix.prepare()
	return &ix, nil
}

// check rejects an index this code can't trust.
func (ix *Index) check() error {
	if ix.Version != Version {
		return fmt.Errorf("format version %d, this Discern reads %d", ix.Version, Version)
	}
	if len(ix.Discs) == 0 {
		return errors.New("it holds no discs")
	}
	if len(ix.Discs) > maxDiscs {
		return fmt.Errorf("%d discs, over the limit of %d", len(ix.Discs), maxDiscs)
	}
	for i, d := range ix.Discs {
		if d.Movie.TMDbID <= 0 {
			return fmt.Errorf("disc %d has no TMDb id", i)
		}
		if len(d.Titles) == 0 || len(d.Titles) > maxTitlesInDisc {
			return fmt.Errorf("disc %d has %d titles", i, len(d.Titles))
		}
		for _, s := range []string{d.Movie.Title, d.Movie.IMDbID, d.Release, d.Name, d.Format, d.ContentHash} {
			if !text(s) {
				return fmt.Errorf("disc %d has a name over %d characters or not UTF-8", i, maxText)
			}
		}
		for _, t := range d.Titles {
			if t.Seconds < 0 || t.Size < 0 || !text(t.File) || !text(t.Type) || !text(t.Name) || len(t.Segments) > 20_000 {
				return fmt.Errorf("disc %d, title %d is out of bounds", i, t.Index)
			}
		}
	}
	return nil
}

func text(s string) bool { return utf8.ValidString(s) && utf8.RuneCountInString(s) <= maxText }

// Write saves the index compressed, as Load reads it.
func (ix *Index) Write(w io.Writer) error {
	zw := gzip.NewWriter(w)
	if err := json.NewEncoder(zw).Encode(ix); err != nil {
		return err
	}
	return zw.Close()
}
