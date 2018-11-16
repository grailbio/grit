// Copyright 2018 GRAIL, Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.
package git

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grailbio/testutil"
)

func TestLog(t *testing.T) {
	dir, cleanup := testutil.TempDir(t, "", "")
	defer cleanup()
	shell(t, dir, `
		git init --bare repo
		git clone repo checkout
		cd checkout
		git config user.email you@example.com
		git config user.name "your name"
		mkdir adir
		echo test file > adir/file1
		echo test file > file1
		git add .
		git commit -m'first commit'
		echo ok > file2
		git add .
		git commit -m'second commit'
		git push
	`)
	repo, err := Open(filepath.Join(dir, "repo"), "adir/", "master")
	if err != nil {
		t.Fatal(err)
	}
	commits, err := repo.Log()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(commits), 1; got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
	c := commits[0]
	if got, want := c.Title(), "first commit"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
	patch, err := repo.Patch(c.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := patch.Subject, "[PATCH] first commit"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, want := patch.Author, `"your name" <you@example.com>`; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, want := len(patch.Diffs), 1; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
	diff := patch.Diffs[0]
	if got, want := diff.Path, "file1"; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
	if !bytes.HasPrefix(diff.Meta, []byte("new file mode 100644\nindex 0000000")) {
		t.Errorf("bad diff meta %s", diff.Meta)
	}
	if !bytes.HasSuffix(diff.Meta, []byte("--- /dev/null\n+++ b/file1")) {
		t.Errorf("bad diff meta %s", diff.Meta)
	}
	if got, want := string(diff.Body), `@@ -0,0 +1 @@
+test file`; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func shell(t *testing.T, dir, script string) {
	t.Helper()
	cmd := exec.Command("bash", "-e", "-x")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(script)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("script failed: %v\n%s", err, stderr.String())
	}
	t.Log(stderr.String())
}
