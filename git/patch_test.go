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
	b, err := ioutil.ReadFile("testdata/0001-reflow-syntax-permit-file-and-dir-module-arguments-v.patch")
	if err != nil {
		t.Fatal(err)
	}
	patch, err := parsePatchHeader(b)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := patch.Write(&buf); err != nil {
		t.Fatal(err)
	}
	patch, err = parsePatchHeader(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}

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
