// Copyright 2018 GRAIL, Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.
package git

import (
	"bytes"
	"context"
	"flag"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/grailbio/testutil"
)

var (
	nocleanup  = flag.Bool("nocleanup", false, "don't clean up git state after tests are run")
	shelltrace = flag.Bool("shelltrace", false, "trace shell execution")
)

func TestLog(t *testing.T) {
	dir, cleanup := testutil.TempDir(t, "", "")
	if *nocleanup {
		log.Println("directory", dir)
	} else {
		defer cleanup()
	}
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

func TestPatchApply(t *testing.T) {
	dir, cleanup := testutil.TempDir(t, "", "")
	if *nocleanup {
		log.Println("directory", dir)
	} else {
		defer cleanup()
	}
	shell(t, dir, `
		mkdir repos
		
		# Set up source repository and add a couple of commits:
		# - add a file to dir1
		# - move this file to dir2
		git init --bare repos/src
		git clone repos/src src
		cd src
		git config user.email you@example.com
		git config user.name "your name"
		mkdir dir1
		echo "test file" > dir1/file1
		git add dir1
		git commit -m'first commit'
		mkdir dir2
		git mv dir1/file1 dir2
		git commit -m'second commit'
		git push
		
		cd ..
		
		# Set up second, empty repository. Note that grit cannot
		# initialize empty repositories, so we add a first commit.
		git init --bare repos/dst
		git clone repos/dst dst
		cd dst
		git config user.email you@example.com
		git config user.name "your name"
		echo license > LICENSE
		git add .
		git commit -m'first commit'
		git push
	`)
	src, err := Open(filepath.Join(dir, "repos/src"), "dir2/", "master")
	if err != nil {
		t.Fatal(err)
	}
	dst, err := Open(filepath.Join(dir, "repos/dst"), "", "master")
	if err != nil {
		t.Fatal(err)
	}
	// Needs to be configured for committer.
	dst.Configure("user.email", "committer@grailbio.com")
	dst.Configure("user.name", "committer")
	commits, err := src.Log()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(commits), 1; got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
	patch, err := src.Patch(commits[0].Digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(patch.Diffs) == 0 {
		t.Fatal("empty patch")
	}
	if err := dst.Apply(patch); err != nil {
		t.Fatalf("failed to apply patch: %v\n%s", err, patch.Patch())
	}
	if err := dst.Push("origin", "master"); err != nil {
		t.Fatal(err)
	}
	// Make sure the file is actually there.
	shell(t, dir, `
		git -C dst pull
		cmp src/dir2/file1 dst/file1 || error file1
	`)
}

func TestLFS(t *testing.T) {
	_, err := exec.LookPath("lfs-test-server")
	if err != nil {
		t.Skip("lfs-test-server not installed")
	}
	dir, cleanup := testutil.TempDir(t, "", "")
	if *nocleanup {
		log.Println("directory", dir)
	} else {
		defer cleanup()
	}

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()
	defer cancel()
	go func() {
		cmd := exec.CommandContext(ctx, "lfs-test-server")
		cmd.Env = []string{
			"LFS_ADMINUSER=user",
			"LFS_ADMINPASS=pass",
			"LFS_CONTENTPATH=" + dir,
		}
		err := cmd.Run()
		if err != nil && err != context.Canceled && !strings.HasSuffix(err.Error(), "signal: killed") {
			log.Panicf("lfs-test-server: %v", err)
		}
		wg.Done()
	}()

	shell(t, dir, `
		mkdir repos

		git init --bare repos/src
		git clone repos/src src
		cd src
		git config user.email you@example.com
		git config user.name "your name"
		git lfs install
		git config -f .lfsconfig lfs.url http://user:pass@localhost:8080
		git add .lfsconfig
		git commit -a -m "lfsconfig"

		echo bigfile >bigfile
		git lfs track bigfile
		git add .
		git commit -a -m "big file"
		git push

		cd ..
		# Create the destination repository. Note that we don't install
		# LFS hooks and instead maintain the pointers manually.
		git init --bare repos/dst
		git clone repos/dst dst
		cd dst
		git config user.email you@example.com
		git config user.name "your name"
		# Manually install the pointer for 'bigfile' into this repository.
		git -C ../src show HEAD:bigfile > bigfile
		git add bigfile
		git commit -m'first commit'
		git push
	`)

	src, err := Open(filepath.Join(dir, "repos/src"), "", "master")
	if err != nil {
		t.Fatal(err)
	}
	ptrs, err := src.ListLFSPointers()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(ptrs), 1; got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got, want := ptrs[0], "bigfile"; got != want {
		t.Fatalf("got %v, want %v", got, want)
	}

	dst, err := Open(filepath.Join(dir, "repos/dst"), "", "master")
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.CopyLFSObject(src, ptrs[0]); err != nil {
		t.Fatal(err)
	}
}

func shell(t *testing.T, dir, script string) {
	t.Helper()
	cmd := exec.Command("bash", "-e", "-x")
	cmd.Dir = dir
	script = `
		function error {
			echo "$@" 1>&2
			exit 1
		}
	` + script
	cmd.Stdin = strings.NewReader(script)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if *shelltrace {
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		if *shelltrace {
			t.Fatal("script failed")
		}
		t.Fatalf("script failed: %v\n%s", err, stderr.String())
	}
	t.Log(stderr.String())
}
