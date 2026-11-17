// Command discern-index builds the compact TheDiscDB index Discern downloads
// (decision D38) from a checkout of https://github.com/TheDiscDb/data:
//
//	discern-index -data ./thediscdb-data -source <commit> -built <commit time> -out discdb-index.json.gz
//
// The index is stamped with -built, TheDiscDB's commit time, not the time it
// is built: the same data then gives the same bytes and the same SHA-256,
// so Discern downloads it again only when TheDiscDB changed.
//
// It writes the index and, next to it, its SHA-256 in the usual
// "<hex>  <file name>" form. Records that don't parse are skipped and
// listed; an index with no discs is an error.
//
// With -sign it also writes the index's signature (decision D59), made with
// the private key in the DISCDB_SIGNING_KEY environment variable (32 bytes
// in base64, the GitHub secret), and checks it with the public key Discern
// trusts, so an index signed with the wrong key is never published.
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

// keyVar holds the signing key's seed.
const keyVar = "DISCDB_SIGNING_KEY"

func main() {
	data := flag.String("data", "", "a checkout of TheDiscDb/data (the folder holding data/movie)")
	out := flag.String("out", "discdb-index.json.gz", "the index to write")
	source := flag.String("source", "", "TheDiscDB's commit, recorded in the index")
	built := flag.String("built", "", "the index's date, RFC 3339: TheDiscDB's commit time (default: now)")
	sign := flag.Bool("sign", false, "also write <out>.sig, signed with the key in "+keyVar)
	flag.Parse()
	if *data == "" {
		fmt.Fprintln(os.Stderr, "usage: discern-index -data <TheDiscDB checkout> [-out file] [-source commit]")
		os.Exit(2)
	}
	when := time.Now()
	if *built != "" {
		t, err := time.Parse(time.RFC3339, *built)
		if err != nil {
			fmt.Fprintln(os.Stderr, "discern-index: -built:", err)
			os.Exit(2)
		}
		when = t
	}
	var seed []byte
	if *sign {
		var err error
		if seed, err = discdb.ParseSeed(os.Getenv(keyVar)); err != nil {
			fmt.Fprintf(os.Stderr, "discern-index: %s: %v\n", keyVar, err)
			os.Exit(2)
		}
	}
	if err := run(*data, *out, *source, when, seed); err != nil {
		fmt.Fprintln(os.Stderr, "discern-index:", err)
		os.Exit(1)
	}
}

// run builds and writes the index, and signs it when seed is set.
func run(data, out, source string, built time.Time, seed []byte) error {
	ix, rep, err := discdb.Build(data, source, built)
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
	if seed != nil {
		sig, err := discdb.Sign(seed, buf.Bytes())
		if err != nil {
			return err
		}
		if err := discdb.Verify(discdb.PublicKey, buf.Bytes(), sig); err != nil {
			return fmt.Errorf("%s isn't the key Discern trusts (discdb.PublicKey): %w", keyVar, err)
		}
		if err := os.WriteFile(out+".sig", sig, 0o644); err != nil {
			return err
		}
	}
	sum := sha256.Sum256(buf.Bytes())
	line := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), filepath.Base(out))
	if err := os.WriteFile(out+".sha256", []byte(line), 0o644); err != nil {
		return err
	}
	fmt.Printf("%d movies, %d discs, %d skipped; %s (%d bytes)\n", rep.Movies, rep.Discs, len(rep.Skipped), out, buf.Len())
	return nil
}
