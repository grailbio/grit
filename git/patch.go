// Copyright 2018 GRAIL, Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package git

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/grailbio/base/digest"
)

const zeroWidthSpace = "\u200b"

// A Diff represents a set of changes to a single file.
type Diff struct {
	// Path holds the path of the file to be changed.
	Path string
	// Meta holds the diff's metadata, treated opaquely.
	Meta []byte
	// Body is the actual diff contents. It is interpreted by
	// git when applying a patch.
	Body []byte
}

// A Patch is a single, atomic change, originating in a Repo. Patches
// comprise one or more diffs, representing file changes in a
// repository. Patches may be derived from commits and applied to a
// repo in order to recreate that commit elsewhere, possibly by way
// of rewriting.
type Patch struct {
	// ID is the commit ID from which the patch was derived.
	ID digest.Digest

	// Author is the patch's author.
	Author string
	// Time is the commit time of the patch's underlying commit.
	Time time.Time
	// Subject is the patch's subject line.
	Subject string
	// Body is the patch's description.
	Body string
	// Diffs contains a set of diffs that represent the patch's
	// change.
	Diffs []Diff
}

func (p Patch) String() string {
	return fmt.Sprintf("patch %s %s %s %s (%d diffs)",
		p.ID.Hex()[:7], p.Author, p.Time, p.Subject, len(p.Diffs))
}

// Paths returns the paths touched by this Patch
// as a set.
func (p Patch) Paths() map[string]bool {
	paths := make(map[string]bool)
	for _, diff := range p.Diffs {
		paths[diff.Path] = true
	}
	return paths
}

// Patch returns the serialized patch as a string.
func (p Patch) Patch() string {
	var b strings.Builder
	_ = p.Write(&b)
	return b.String()
}

// Write serializes the patch to the standard git patch format and
// writes it to the provided writer. Write escapes diff-like content
// in the patch body. Specifically, lines beginning with "diff",
// "---", and "+++" are prefixed with a unicode zero width space.
// This is to avoid ambiguity in git's patch parsing. This appears to
// be an issue with git itself: patches that contain other patches
// embedded in the patch description fail to apply properly using
// standard git tooling.
func (p Patch) Write(w io.Writer) error {
	ew := &errWriter{Writer: w}
	fmt.Fprintf(ew, "From %s Mon Sep 17 00:00:00 2001\n", p.ID.Hex())
	fmt.Fprintf(ew, "From: %s\n", p.Author)
	fmt.Fprintf(ew, "Date: %s\n", p.Time.Format(gitTimeLayout))
	fmt.Fprintf(ew, "Subject: %s\n", p.Subject)
	body := strings.Replace(p.Body, "\ndiff", "\n"+zeroWidthSpace+"diff", -1)
	body = strings.Replace(body, "\n---", "\n"+zeroWidthSpace+"---", -1)
	body = strings.Replace(body, "\n+++", "\n"+zeroWidthSpace+"+++", -1)
	fmt.Fprintf(ew, "\n%s\n---\n\n\n", body)
	for _, diff := range p.Diffs {
		fmt.Fprintf(ew, "diff --git a/%s b/%s\n", diff.Path, diff.Path)
		ew.Write(diff.Meta)
		ew.Write([]byte{'\n'})
		ew.Write(diff.Body)
		ew.Write([]byte{'\n'})
	}
	return ew.Err()
}

var oid = []byte("oid")

// MaybeContainsLFSPointer uses (coarse) heuristics to determine
// whether the patch could possibly contain an LFS pointer. If it
// returns false, then there is definitely not an LFS pointer in the
// patch.
func (p Patch) MaybeContainsLFSPointer() bool {
	for _, diff := range p.Diffs {
		// This is definitely hacky, but works well enough. These are
		// required fields in any LFS pointer file, and any change
		// involving a new LFS object must declare an oid.
		if bytes.Contains(diff.Body, oid) {
			return true
		}
	}
	return false
}

var errMalformedPatch = errors.New("malformed patch")
var continueHeader = []byte(" ")

// ParsePatchHead parses a patch header from the provided buffer.
func parsePatchHeader(b []byte) (Patch, error) {
	from := scanLine(&b)
	fields := bytes.Fields(from)
	if len(fields) < 2 {
		return Patch{}, errMalformedPatch
	}
	var (
		p   Patch
		err error
	)
	p.ID, err = SHA1.Parse(string(fields[1]))
	if err != nil {
		return Patch{}, err
	}
	m, err := mail.ReadMessage(bytes.NewReader(b))
	if err != nil {
		return Patch{}, err
	}
	addrs, err := m.Header.AddressList("From")
	if err != nil {
		return Patch{}, err
	}
	if len(addrs) != 1 {
		return Patch{}, errMalformedPatch
	}
	p.Author = addrs[0].String()
	p.Time, err = m.Header.Date()
	if err != nil {
		return Patch{}, err
	}
	p.Subject = m.Header.Get("Subject")
	if p.Subject == "" {
		return Patch{}, errors.New("patch is missing subject")
	}
	b, err = ioutil.ReadAll(m.Body)
	if err != nil {
		return Patch{}, err
	}
	p.Body = string(b)
	return p, nil
}

func scan(b *[]byte, prefix string) (body []byte) {
	body = next(b, prefix)
	if len(*b) >= len(prefix) {
		*b = (*b)[len(prefix):]
	}
	return body
}

func scanLine(b *[]byte) (line []byte) {
	i := bytes.Index(*b, []byte{'\n'})
	if i < 0 {
		line = *b
		*b = nil
		return
	}
	line = (*b)[:i]
	*b = (*b)[i+1:]
	return
}

func next(b *[]byte, prefix string) (body []byte) {
	if bytes.HasPrefix(*b, []byte(prefix)) {
		return nil
	}
	i := bytes.Index(*b, []byte("\n"+prefix))
	if i < 0 {
		body = *b
		*b = nil
		return
	}
	body = (*b)[:i]
	*b = (*b)[i+1:]
	return
}

func foreach(b []byte, prefix string, do func(section []byte) error) error {
	if !bytes.HasPrefix(b, []byte(prefix)) {
		i := bytes.Index(b, []byte("\n"+prefix))
		if i < 0 {
			return nil
		}
		b = b[i+1:]
	}
	for {
		i := bytes.Index(b, []byte("\n"+prefix))
		if i < 0 {
			return do(b)
		}
		if err := do(b[:i]); err != nil {
			return err
		}
		b = b[i+1:]
	}
}

var headerRe = regexp.MustCompile(`^([[:alnum:]]+): (.*)$`)

func parseHeader(line []byte) (header, value []byte, ok bool) {
	g := headerRe.FindSubmatch(line)
	if g == nil {
		return nil, nil, false
	}
	return g[1], g[2], true
}

var diffHeaderRe = regexp.MustCompile(`^diff --git a/([^ ]+)`)

func parseDiffHeader(line []byte) (path []byte) {
	g := diffHeaderRe.FindSubmatch(line)
	if g == nil {
		return nil
	}
	return g[1]
}

type errWriter struct {
	io.Writer
	err error
}

func (e *errWriter) Err() error {
	return e.err
}

func (e *errWriter) Write(p []byte) (n int, err error) {
	if e.err != nil {
		return 0, e.err
	}
	n, err = e.Writer.Write(p)
	e.err = err
	return
}
