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
	patch, err := parsePatch(b)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := patch.Write(&buf); err != nil {
		t.Fatal(err)
	}
	patch, err = parsePatch(buf.Bytes())
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
	if got, want := len(patch.Diffs), 6; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, want := patch.Diffs[0].Path, "go/src/github.com/grailbio/reflow/syntax/BUILD"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, want := string(patch.Diffs[0].Meta), `index 86feb7e84b..07a2f4bb7e 100644
--- a/go/src/github.com/grailbio/reflow/syntax/BUILD
+++ b/go/src/github.com/grailbio/reflow/syntax/BUILD`; got != want {
		t.Errorf("got %v, want %v", got, want)
	}

	if got, want := string(patch.Diffs[0].Body), `@@ -49,6 +49,7 @@ go_test(
         "bundle_test.go",
         "digest_test.go",
         "eval_test.go",
+        "module_test.go",
         "parse_test.go",
         "pat_test.go",
         "reqs_test.go",`; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}
