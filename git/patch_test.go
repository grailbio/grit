// Copyright 2018 GRAIL, Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.
package git

import (
	"bytes"
	"io/ioutil"
	"testing"
	"time"
)

func TestParsePatch(t *testing.T) {
	patch := parsePatchRoundTrip(t, "testdata/0001-reflow-syntax-permit-file-and-dir-module-arguments-v.patch")
	if got, want := patch.ID.Hex(), "b969e1d8eb27e72eee131c1d31398fc3e6ef9c25"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, want := patch.Author, `"marius a. eriksen" <marius@grailbio.com>`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := patch.Time.Format(time.Kitchen), "11:44AM"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestParsePatchInvalidEmail verifies that we can parse patches with invalid
// email addresses, as these can be written by `git format-patch`.
func TestParsePatchInvalidEmail(t *testing.T) {
	// This patch has an email address with the '[' character, which is invalid.
	// See https://tools.ietf.org/html/rfc5322#section-3.2.3.
	patch := parsePatchRoundTrip(t, "testdata/0001-build-deps-bump-activesupport-from-6.0.2.1-to-6.0.3..patch")
	if got, want := patch.Author, `"dependabot[bot]" <49699333+dependabot[bot]@users.noreply.github.com>`; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// parsePatchRoundTrip parses and returns the patch at path, with a round trip
// through (Patch).Write.
func parsePatchRoundTrip(t *testing.T, path string) Patch {
	t.Helper()
	b, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %q: %v", path, err)
	}
	patch, err := parsePatchHeader(b)
	if err != nil {
		t.Fatalf("failed to parse patch: %v", err)
	}
	var buf bytes.Buffer
	if err := patch.Write(&buf); err != nil {
		t.Fatalf("failed to write to byte buffer: %v", err)
	}
	patch, err = parsePatchHeader(buf.Bytes())
	if err != nil {
		t.Fatalf("failed to parse written patch (roundtrip failed): %v", err)
	}
	return patch
}
