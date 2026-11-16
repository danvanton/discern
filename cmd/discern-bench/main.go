// Command discern-bench scores the title selector on TheDiscDB's movie
// discs (DESIGN section 8, decisions D50 and D51).
//
// Each disc's record lists its titles as MakeMKV showed them and says which
// is the main movie: a ready-made answer key. The selector uses TheDiscDB
// too, so the main run is leave-one-out: each disc's own release, and every
// disc with the same ContentHash (a double feature is filed under both its
// movies), is hidden from the index before the selector sees the disc. That
// measures a disc TheDiscDB doesn't know. A second run with the index whole
// measures a known disc.
//
//	discern-bench -index discdb-index.json.gz [-json out.json] [-gate] [-compare base.json] [-list]
//
// With -gate it fails if wrong is over 0.5% of the discs the leave-one-out
// run would act on alone. With -compare (CI's gate) it also fails if that
// run picks more wrong titles than the base commit did on the same index.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/danvanton/discern/internal/discdb"
	"github.com/danvanton/discern/internal/makemkv"
	"github.com/danvanton/discern/internal/selector"
)

// maxWrongShare is the design's target: under 0.5% wrong among the discs
// the selector would act on alone.
const maxWrongShare = 0.005

func main() {
	indexPath := flag.String("index", "", "TheDiscDB index (discdb-index.json.gz)")
	jsonOut := flag.String("json", "", "write the scores as JSON to this file")
	compare := flag.String("compare", "", "scores of the base commit (JSON) to gate against")
	list := flag.Bool("list", false, "list every disc the leave-one-out run gets wrong")
	gate := flag.Bool("gate", false, "fail if wrong is over the 0.5% target (implied by -compare)")
	flag.Parse()
	if err := run(*indexPath, *jsonOut, *compare, *list, *gate, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "discern-bench:", err)
		os.Exit(1)
	}
}

func run(indexPath, jsonOut, compare string, list, gate bool, out io.Writer) error {
	if indexPath == "" {
		return errors.New("-index is required")
	}
	ix, err := discdb.Load(indexPath)
	if err != nil {
		return err
	}
	res := Bench(ix)
	res.Print(out)
	if list {
		for _, w := range res.Wrong {
			fmt.Fprintln(out, "wrong:", w)
		}
	}
	if jsonOut != "" {
		b, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(jsonOut, append(b, '\n'), 0o644); err != nil {
			return err
		}
	}
	if compare != "" {
		b, err := os.ReadFile(compare)
		if err != nil {
			return err
		}
		var base Result
		if err := json.Unmarshal(b, &base); err != nil {
			return fmt.Errorf("%s: %w", compare, err)
		}
		return Gate(base, res)
	}
	if gate {
		return Target(res)
	}
	return nil
}

// Result is one benchmark run's scores.
type Result struct {
	Index  string    `json:"index"` // TheDiscDB's commit the index was built from
	Built  time.Time `json:"built"`
	Unseen Run       `json:"unseen"` // leave-one-out
	Known  Run       `json:"known"`  // the index whole
	// Longest is the baseline: the longest title, always acted on.
	Longest Score `json:"longest"`
	// Doubtful are discs whose answer itself looks wrong (decision D53,
	// selector.DoubtfulMain): not scored, and listed.
	Doubtful []string `json:"doubtful"`
	// Wrong lists the unseen run's wrong picks, for -list.
	Wrong []string `json:"-"`
}

// Run is one pass over the discs.
type Run struct {
	OneMain Score `json:"one_main"` // discs with one MainMovie: the gate
	TwoMain Cuts  `json:"two_main"` // discs with two or more: reported only
}

// Score counts discs with one main movie.
type Score struct {
	Right     int `json:"right"`
	Abstained int `json:"abstained"` // held for the user (confidence Low)
	Wrong     int `json:"wrong"`     // acted alone on another title
}

// Cuts counts discs with two or more main titles (decision D41).
type Cuts struct {
	Held   int `json:"held"`   // asked, the right way
	Picked int `json:"picked"` // acted alone on one of the cuts
	Wrong  int `json:"wrong"`  // acted alone on another title
}

func (s Score) total() int { return s.Right + s.Abstained + s.Wrong }

// WrongShare is wrong among the discs acted on alone.
func (s Score) WrongShare() float64 {
	if s.Right+s.Wrong == 0 {
		return 0
	}
	return float64(s.Wrong) / float64(s.Right+s.Wrong)
}

// Bench runs both passes over every disc that names a main movie.
func Bench(ix *discdb.Index) Result {
	res := Result{Index: ix.Source, Built: ix.Built}
	for i := range ix.Discs {
		d := &ix.Discs[i]
		mains := mainTitles(d)
		if len(mains) == 0 {
			continue // no answer to score against
		}
		if len(mains) == 1 {
			if long, ok := selector.DoubtfulMain(mains[0], d.Titles); ok {
				res.Doubtful = append(res.Doubtful, fmt.Sprintf("%s (%d), %s, disc %q: TheDiscDB's movie is title %d (%d s), but title %d (%d s) is unclassified",
					d.Movie.Title, d.Movie.Year, d.Release, d.Name, mains[0].Index, mains[0].Seconds, long.Index, long.Seconds))
				continue
			}
		}
		scan := d.Scan()
		hide := func(k *discdb.Disc) bool { return sameDisc(d, k) }
		unseen := decide(ix, scan, hide)
		known := decide(ix, scan, nil)
		if len(mains) == 1 {
			if !res.Unseen.OneMain.add(scan, mains[0], unseen) {
				res.Wrong = append(res.Wrong, fmt.Sprintf("%s (%d), %s, disc %q: picked %v, the movie is title %d; %s",
					d.Movie.Title, d.Movie.Year, d.Release, d.Name, unseen.Titles, mains[0].Index, unseen.Reason))
			}
			res.Known.OneMain.add(scan, mains[0], known)
			long := scan.Titles[0]
			for _, t := range scan.Titles[1:] {
				if t.Duration > long.Duration || t.Duration == long.Duration && t.SizeBytes > long.SizeBytes {
					long = t
				}
			}
			if sameVideo(scan, long.Index, mains[0]) {
				res.Longest.Right++
			} else {
				res.Longest.Wrong++
			}
			continue
		}
		res.Unseen.TwoMain.add(scan, mains, unseen)
		res.Known.TwoMain.add(scan, mains, known)
	}
	return res
}

// decide is what steps.Select does with the index.
func decide(ix *discdb.Index, scan *makemkv.Disc, skip func(*discdb.Disc) bool) selectorResult {
	m, tied := ix.Lookup(scan, skip)
	return selector.Decide(selector.Input{Disc: scan, Match: m, Tied: tied, Indexed: true})
}

// add scores one decision; it returns false if the pick was wrong.
func (s *Score) add(scan *makemkv.Disc, main discdb.Title, sel selectorResult) bool {
	switch {
	case actsAlone(sel) && len(sel.Titles) == 1 && sameVideo(scan, sel.Titles[0], main):
		s.Right++
	case !actsAlone(sel):
		s.Abstained++
	default:
		s.Wrong++
		return false
	}
	return true
}

func (c *Cuts) add(scan *makemkv.Disc, mains []discdb.Title, sel selectorResult) {
	switch {
	case !actsAlone(sel):
		c.Held++
	case len(sel.Titles) == 1 && slices.ContainsFunc(mains, func(m discdb.Title) bool { return sameVideo(scan, sel.Titles[0], m) }):
		c.Picked++
	default:
		c.Wrong++
	}
}

// Gate fails when the unseen run is worse than base's on the same index, or
// over the design's target.
func Gate(base, head Result) error {
	if base.Index != head.Index || !base.Built.Equal(head.Built) {
		return fmt.Errorf("the base scores are from another index (%s, %s), not %s, %s",
			base.Index, base.Built.Format(time.RFC3339), head.Index, head.Built.Format(time.RFC3339))
	}
	b, h := base.Unseen.OneMain, head.Unseen.OneMain
	if h.Wrong > b.Wrong {
		return fmt.Errorf("wrong picks went up from %d to %d (discs TheDiscDB doesn't know)", b.Wrong, h.Wrong)
	}
	return Target(head)
}

// Target fails when the unseen run is wrong on over 0.5% of the discs it
// would act on alone.
func Target(head Result) error {
	if s := head.Unseen.OneMain; s.WrongShare() > maxWrongShare {
		return fmt.Errorf("%.2f%% wrong among the discs it would act on alone, over the target of %.1f%%",
			100*s.WrongShare(), 100*maxWrongShare)
	}
	return nil
}

// Print writes the scores as a Markdown table, for CI's job summary.
func (r Result) Print(w io.Writer) {
	pct := func(n, of int) string {
		if of == 0 {
			return "-"
		}
		return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(of))
	}
	row := func(name string, s Score) {
		n := s.total()
		fmt.Fprintf(w, "| %s | %d | %d (%s) | %d (%s) | %d (%s) | %.2f%% |\n", name, n,
			s.Right, pct(s.Right, n), s.Abstained, pct(s.Abstained, n), s.Wrong, pct(s.Wrong, n), 100*s.WrongShare())
	}
	fmt.Fprintf(w, "TheDiscDB index %s, built %s\n\n", r.Index, r.Built.Format(time.DateOnly))
	fmt.Fprintln(w, "| Discs with one main movie | Discs | Right | Asked | Wrong | Wrong when acting alone |")
	fmt.Fprintln(w, "|---|---|---|---|---|---|")
	row("Unseen by TheDiscDB (the gate)", r.Unseen.OneMain)
	row("Known to TheDiscDB", r.Known.OneMain)
	row("Baseline: longest title", r.Longest)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| Discs with two main titles | Asked | Picked a cut | Wrong |")
	fmt.Fprintln(w, "|---|---|---|---|")
	for _, c := range []struct {
		name string
		c    Cuts
	}{{"Unseen", r.Unseen.TwoMain}, {"Known", r.Known.TwoMain}} {
		fmt.Fprintf(w, "| %s | %d | %d | %d |\n", c.name, c.c.Held, c.c.Picked, c.c.Wrong)
	}
	fmt.Fprintf(w, "\nNot scored, because TheDiscDB's answer looks wrong: %d\n", len(r.Doubtful))
	for _, d := range r.Doubtful {
		fmt.Fprintf(w, "- %s\n", d)
	}
}
