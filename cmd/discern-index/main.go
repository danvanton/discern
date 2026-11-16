// Command discern-index builds the compact TheDiscDB index Discern downloads
// (decision D38) from a checkout of https://github.com/TheDiscDb/data:
//
//	discern-index -data ./thediscdb-data -source <commit> -out discdb-index.json.gz
//
// It writes the index and, next to it, its SHA-256 in the usual
// "<hex>  <file name>" form. Records that don't parse are skipped and
// listed; an index with no discs is an error.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/danvanton/discern/internal/discdb"
)

func main() {
	data := flag.String("data", "", "a checkout of TheDiscDb/data (the folder holding data/movie)")
	out := flag.String("out", "discdb-index.json.gz", "the index to write")
	source := flag.String("source", "", "TheDiscDB's commit, recorded in the index")
	flag.Parse()
	if *data == "" {
		fmt.Fprintln(os.Stderr, "usage: discern-index -data <TheDiscDB checkout> [-out file] [-source commit]")
		os.Exit(2)
	}
	if err := run(*data, *out, *source); err != nil {
		fmt.Fprintln(os.Stderr, "discern-index:", err)
		os.Exit(1)
	}
}

func run(data, out, source string) error {
	ix, rep, err := discdb.Build(data, source, time.Now())
	if err != nil {
		return err
	}
	for _, s := range rep.Skipped {
		fmt.Fprintln(os.Stderr, "skipped:", s)
	}
	var buf bytes.Buffer
	if err := ix.Write(&buf); err != nil {
		return err
	}
	// Read it back as Discern will, so a bad index is never published.
	if _, err := discdb.Read(bytes.NewReader(buf.Bytes())); err != nil {
		return fmt.Errorf("the index doesn't read back: %w", err)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o644); err != nil {
		return err
	}
	sum := sha256.Sum256(buf.Bytes())
	line := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(out))
	if err := os.WriteFile(out+".sha256", []byte(line), 0o644); err != nil {
		return err
	}
	fmt.Printf("%d movies, %d discs, %d skipped; %s (%d bytes)\n", rep.Movies, rep.Discs, len(rep.Skipped), out, buf.Len())
	return nil
}
