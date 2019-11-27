// Copyright 2018 GRAIL, Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package main_test

import (
	"flag"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/grailbio/testutil"
)

var (
	tracecmd  = flag.Bool("tracecmd", false, "trace commands")
	nocleanup = flag.Bool("nocleanup", false, "don't clean up test temp directories")
)

func TestGrit(t *testing.T) {
	dir, cleanup := temp(t)
	defer cleanup()
	var g grit
	g.Build(t)

	var (
		repoA = filepath.Join(dir, "arepo")
		repoB = filepath.Join(dir, "brepo")
	)

	run(t, "git", "init", "--bare", repoA)
	run(t, "git", "init", "--bare", repoB)

	a := repo(filepath.Join(dir, "a"))
	b := repo(filepath.Join(dir, "b"))
	a.Clone(t, filepath.Join(dir, "arepo"))
	b.Clone(t, filepath.Join(dir, "brepo"))

	// Grit doesn't (yet?) handle empty repos, so we initialize B with a commit.
	b.Git(t, "commit", "--allow-empty", "-m", "initial commit")
	b.Git(t, "push")

	a.WriteFile(t, "file1", "content 1")
	a.Git(t, "add", ".")
	a.Git(t, "commit", "-a", "-m", "first commit")
	a.Git(t, "push")

	g.Run(t, "-push", repoA, repoB)
	b.Git(t, "pull")
	a.Compare(t, b)

	a.WriteFile(t, "file2", "content 2")
	a.Git(t, "add", ".")
	a.Git(t, "commit", "-a", "-m", "second commit")
	a.Git(t, "push")

	g.Run(t, "-push", repoA, repoB)
	b.Git(t, "pull")
	a.Compare(t, b)

	// Now try to sync the other way.
	b.WriteFile(t, "file3", "content 3")
	b.Git(t, "add", ".")
	b.Git(t, "commit", "-a", "-m", "commit from b")
	b.Git(t, "push")

	g.Run(t, "-push", repoB, repoA)
	a.Git(t, "pull")
	a.Compare(t, b)

	// Pushing the other way should now be a no-op.
	g.Run(t, "-push", repoA, repoB)
	b.Git(t, "pull")
	a.Compare(t, b)
}

// TestGritRules ensures that rules are applied universally across
// grit actions.
func TestGritRules(t *testing.T) {
	dir, cleanup := temp(t)
	defer cleanup()
	var g grit
	g.Build(t)

	var (
		repoHome   = filepath.Join(dir, "home")
		repoRemote = filepath.Join(dir, "remote")
	)

	run(t, "git", "init", "--bare", repoHome)
	run(t, "git", "init", "--bare", repoRemote)

	home := repo(filepath.Join(dir, "home_checkout"))
	remote := repo(filepath.Join(dir, "remote_checkout"))

	home.Clone(t, filepath.Join(dir, "home"))
	remote.Clone(t, filepath.Join(dir, "remote"))

	for _, r := range []repo{home, remote} {
		r.Git(t, "commit", "--allow-empty", "-m", "initial commit")
		r.Git(t, "push")
	}

	remote.WriteFile(t, "file1", "content1")
	remote.Git(t, "add", ".")
	remote.Git(t, "commit", "-a", "-m", "commit 1 to remote")
	remote.Git(t, "push")
	g.Run(t, "-push", repoRemote, repoHome+",remote/")

	home.Git(t, "pull")

	// Now synthesize a change to the home repo that touches the remote/
	// directory but is excluded from the sync rules. We fabricate a
	// source ID to emulate that it has been pushed from a different
	// remote repository.

	home.WriteFile(t, "remote/BUILD", "excluded content")
	home.WriteFile(t, "test.txt", "out of scope content")
	home.Git(t, "add", ".")
	home.Git(t, "commit", "-a", "-m", "commit 1 to local\n\nfbshipit-source-id: bb96450\n")
	home.Git(t, "push")

	remote.WriteFile(t, "file2", "content2")
	remote.Git(t, "add", ".")
	remote.Git(t, "commit", "-a", "-m", "commit 1 2 remote")
	remote.Git(t, "push")

	g.Run(t, "-push", repoRemote, repoHome+",remote/", "strip:^BUILD$")

	home.Git(t, "pull")

	repo(filepath.Join(string(home), "remote")).Compare(t, remote, "BUILD")
}

func temp(t *testing.T) (dir string, cleanup func()) {
	t.Helper()
	dir, cleanup = testutil.TempDir(t, "", "")
	if *nocleanup {
		log.Printf("%s dir: %v", t.Name(), dir)
		cleanup = func() {}
	}
	return dir, cleanup
}

type repo string

func (r repo) Clone(t *testing.T, url string) {
	t.Helper()
	dir := filepath.Dir(string(r))
	base := filepath.Base(string(r))
	run(t, "git", "-C", dir, "clone", url, base)
	r.Git(t, "config", "user.email", "you@example.com")
	r.Git(t, "config", "user.name", "your name")
}

func (r repo) Git(t *testing.T, arg ...string) {
	t.Helper()
	run(t, "git", append([]string{"-C", string(r)}, arg...)...)
}

func (r repo) Run(t *testing.T, name string, arg ...string) {
	t.Helper()
	cmd := exec.Command(name, arg...)
	cmd.Dir = string(r)
	runCommand(t, cmd)
}

func (r repo) WriteFile(t *testing.T, path, content string) {
	t.Helper()
	path = filepath.Join(string(r), path)
	_ = os.MkdirAll(filepath.Dir(path), 0777)
	if err := ioutil.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatalf("%s: write %s: %v", r, path, err)
	}
}

func (r repo) Compare(t *testing.T, q repo, excludes ...string) {
	t.Helper()
	var args []string
	for _, x := range excludes {
		args = append(args, "-x", x)
	}
	args = append(args, "-x", `\.git`)
	args = append(args, string(r), string(q))
	run(t, "diff", args...)
}

type grit string

func (g *grit) Build(t *testing.T) {
	t.Helper()
	*g = grit(testutil.GoExecutable(t, "//go/src/github.com/grailbio/grit/grit"))
}

func (g grit) Run(t *testing.T, arg ...string) {
	t.Helper()
	args := append([]string{"-config=user.name=test,user.email=you@example.com"}, arg...)
	run(t, string(g), args...)
}

func run(t *testing.T, name string, arg ...string) {
	t.Helper()
	runCommand(t, exec.Command(name, arg...))
}

func runCommand(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	if *tracecmd {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		log.Printf("run %s %v", cmd.Path, cmd.Args)
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s %v: %s", cmd.Path, cmd.Args, err)
		}
		return
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("grit %s %v: %s\n%s", cmd.Path, cmd.Args, err, out)
	}
}
