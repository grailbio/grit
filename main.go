// Copyright 2018 GRAIL, Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Grit copies commits from a source repository to a destination
// repository. It is intended to mirror projects residing in an
// private monorepo to an external project-specific Git repository.
//
// Usage:
//
// 	grit [-push] [-dump] [-linearize] src dst rules...
//
// "grit -push src dst rules..." copies commits from the repository
// src to the repository dst, applying the the given rules and, if
// successful, pushes the changes to the destination repository.
// Repositories are named by url, prefix, and branch, with one of the
// following syntaxes:
//
// 	url
// 	url,prefix
// 	url,prefix,branch
//
// The default prefix is "" and the default branch is "master". When a
// prefix is specified, Grit considers constructs a view of the repository
// limited to the given prefix path. Changes outside of this prefix are
// discarded.
//
// Linearization
//
// If the flag -linearize is provided, then the source repository's
// history is linearized before copying commits. Linearization is
// done by ensuring that every commit has a single parent, so that
// the repository contains no merge commits. This is useful to ensure
// that grit can cleanly apply patches from repositories whose
// histories are not linear (e.g., when accepting patches from
// GitHub).
//
// Rules
//
// Grit can apply a set of rewrite rules to source commits before
// they are copied to the destination repository. Rules are specified
// as "kind:param". Rules kinds are:
//
//	strip:regexp
//		Strips diffs applied to files matching the given regular
//		expression.
//
//	rewrite:regexp:/old_re/new_re/
//    For each file whose path matches regexp, regexp-replace each line in the
//    file from old_re to new_re. For example, rule
//
//        rewrite:go.mod$:/replace .* => .*//
//
//    will remove all "replace from => to" directives from go.mod
//    files.  The 2nd letter after the path regexp ('/' in the example)
//    determines the separator character for the old and the new regexps. The
//    previous example can also be written as
//
//        rewrite:go.mod$:!replace .* => .*!!
//
// Example: one-way sync
//
// Copy commits from the "project/" directory in repository
// ssh://git@git.company.com/foo.git to the root directory in the
// repository https://github.com/company/project.git. Diffs applied
// to files named BUILD are skipped.
//
// 	grit -push ssh://git@git.company.com/foo.git,project/ \
//		https://github.com/company/project.git "strip:^BUILD$" "strip:/BUILD$"
//
// Example: two-way sync
//
// Assume we want to sync bidirectionally between two repositories:
//
//  repoA=ssh://git@company.example.com,go/src/github.com/grailbio/project.git
//  repoB=ssh://git@github.com:github.com/grailbio/project.git
//
// We usually develop on repoA and mirror changes to repoB. We also want to
// accept external contributions or upstream changes from repoB and push them to
// repoA. To sync from repoA to repoB, do the following:
//
//  grit -push $repoA $repoB
//
// To sync from repoB to repo A, do the following:
//
//  # Pull changes from repoB to repoA. But don't push it automatically, since we want to
//  # review them internally.
//  grit $repoB $repoA
//  # grailXXXXX is the copy of repoA managed by grit
//  cd /var/tmp/grailXXXXX
//  # Squash changes into one
//  git reset --soft origin/master && git commit --edit -m"$(git log --reverse HEAD..HEAD@{1})"
//  # Start a regular code review process.
//  arc diff
//  # After the review is accepted, land the changes.
//  arc land

package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/grailbio/base/log"
	"github.com/grailbio/grit/git"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
	grit src dst rules...
	grit -push src dst rules...
	grit -dump src dst rules`)
	flag.PrintDefaults()
	os.Exit(2)
}

type rewriteRule struct {
	pathRe *regexp.Regexp // matched against the pathname
	oldRe  *regexp.Regexp // matched against each line in the file
	new    []byte         // replacement
}

func parseRewriteRule(rule string) (r rewriteRule) {
	parts := strings.SplitN(rule, ":", 2)
	if len(parts) != 2 {
		log.Fatalf("invalid rewrite rule %s", rule)
	}
	var err error
	if r.pathRe, err = regexp.Compile(parts[0]); err != nil {
		log.Fatalf("rewrite: invalid path regexp %s: %s", parts[0], err)
	}
	if len(parts[1]) < 3 {
		log.Fatalf("rewrite: rule '%s' must be of form rewrite:pathre:/from_re/to_re/", rule)
	}
	sep := parts[1][0:1]
	parts = strings.Split(parts[1][1:], sep)
	if len(parts) != 3 || parts[2] != "" {
		log.Fatalf("rewrite: rule '%s' must be of form rewrite:pathre:/from_re/to_re/", rule)
	}
	if r.oldRe, err = regexp.Compile(parts[0]); err != nil {
		log.Fatalf("rewrite: invalid 'from' regexp %s: %s", parts[0], err)
	}
	r.new = []byte(parts[1])
	return r
}

func (r *rewriteRule) rewrite(diff []byte) []byte {
	result := bytes.Buffer{}
	for _, line := range bytes.Split(diff, []byte("\n")) {
		line = r.oldRe.ReplaceAll(line, r.new)
		result.Write(line)
		result.WriteByte('\n')
	}
	return result.Bytes()
}

func main() {
	log.SetPrefix("")
	log.AddFlags()
	dump := flag.Bool("dump", false, "dump patches to stdout instead of applying them to the destination repository")
	push := flag.Bool("push", false, "push applied changes to the destination repository's remote")
	configs := flag.String("config", "", "comma-separated key-value pairs that should be passed to git")
	linearize := flag.Bool("linearize", false, "linearize source repository history before copying commits")
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() < 2 {
		flag.Usage()
	}
	if *push && *dump {
		flag.Usage()
	}
	srcURL, srcPrefix, srcBranch := parseSpec(flag.Arg(0))
	dstURL, dstPrefix, dstBranch := parseSpec(flag.Arg(1))
	if srcURL == dstURL {
		log.Error.Printf("source and destination cannot be the same")
		flag.Usage()
	}

	var strip []*regexp.Regexp
	var rewrite []rewriteRule
	rules := flag.Args()[2:]
	for _, rule := range rules {
		parts := strings.SplitN(rule, ":", 2)
		if len(parts) != 2 {
			log.Fatalf("invalid rule %s", rule)
		}
		switch parts[0] {
		case "strip":
			r, err := regexp.Compile(parts[1])
			if err != nil {
				log.Fatalf("invalid regexp %s: %s", parts[1], err)
			}
			strip = append(strip, r)
		case "rewrite":
			rewrite = append(rewrite, parseRewriteRule(parts[1]))
			if len(parts) != 2 {
				log.Fatalf("invalid rule %s", rule)
			}
		default:
			log.Fatalf("invalid rule type %s", parts[0])
		}
	}

	log.Printf("synchronizing repo:%s prefix:%s branch:%s -> repo:%s prefix:%s branch:%s",
		srcURL, srcPrefix, srcBranch, dstURL, dstPrefix, dstBranch)
	open := func(url, prefix, branch string) *git.Repo {
		r, err := git.Open(url, prefix, branch)
		if err != nil {
			log.Fatalf("open %s: %v", url, err)
		}
		for _, kv := range strings.Split(*configs, ",") {
			if kv == "" {
				continue
			}
			parts := strings.SplitN(kv, "=", 2)
			if len(parts) != 2 {
				log.Fatalf("bad config %s", kv)
			}
			r.Configure(parts[0], parts[1])
		}
		return r
	}
	// Open repositories in URL order so that we don't deadlock across
	// multiple repositories.
	var src, dst *git.Repo
	if srcURL < dstURL {
		src = open(srcURL, srcPrefix, srcBranch)
		dst = open(dstURL, dstPrefix, dstBranch)
	} else {
		dst = open(dstURL, dstPrefix, dstBranch)
		src = open(srcURL, srcPrefix, srcBranch)
	}
	defer src.Close()
	defer dst.Close()

	if *linearize {
		if err := src.Linearize(); err != nil {
			log.Fatalf("linearize %s: %v", src, err)
		}
	}

	// Last synchronized commit, if any.
	last, err := dst.Log("-1", "--grep", `^\s*\(fb\)\?shipit-source-id: [a-z0-9]\+$`)
	if err != nil {
		log.Fatalf("log %s: %v", dst, err)
	}
	var commits []*git.Commit
	if len(last) == 0 {
		log.Printf("performing initial sync")
		commits, err = src.Log("--no-merges")
		if err != nil {
			log.Fatalf("log %s: %v", src, err)
		}
	} else {
		log.Printf("synchronizing: last diff: %v, source: %v", last[0].Digest, last[0].ShipitID())
		ids := last[0].ShipitID()
		if len(ids) == 0 {
			log.Fatalf("no fbshipid-resource-id found in commit: %+v", last[0])
		}
		// When a commit is a squash of multiple commits, they are sorted in
		// ascending chronological order. So the last ID is the one we should sync
		// from.
		newestID := ids[len(ids)-1]
		commits, err = src.Log(newestID+"..master", "--ancestry-path", "--no-merges")
		if err != nil {
			log.Fatalf("log %s: %v", src, err)
		}
	}
	// Filter out commits which are themselves copies, so that
	// we can properly support multi-way syncing.
	raw := commits
	commits = nil
	for _, commit := range raw {
		if len(commit.ShipitID()) == 0 {
			commits = append(commits, commit)
		}
	}

	log.Printf("%d commits to copy", len(commits))
	var ncommit int
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		patch, err := src.Patch(c.Digest, dst.Prefix())
		if err != nil {
			log.Fatalf("%s: patch %s: %v", src, c.Digest.Hex()[:7], err)
		}
		if patch.Body != "" {
			patch.Body += "\n\n"
		}
		patch.Body += fmt.Sprintf("fbshipit-source-id: %s", patch.ID.Hex()[:7])
		// Filter out BUILD files and files that begin with "grail_internal".
		// Prefixes are already rewritten by the repo.
		var diffs []git.Diff
	diffloop:
		for _, diff := range patch.Diffs {
			for _, r := range strip {
				if r.MatchString(diff.Path) {
					log.Debug.Printf("file %s matches rule %s: stripping", diff.Path, r)
					continue diffloop
				}
			}
			for _, r := range rewrite {
				if r.pathRe.MatchString(diff.Path) {
					diff.Body = r.rewrite(diff.Body)
				}
			}
			diffs = append(diffs, diff)
		}
		if len(diffs) == 0 {
			log.Printf("skipping empty patch %s", patch.ID.Hex()[:7])
			continue
		}
		ncommit++
		patch.Diffs = diffs
		if *dump {
			if err := patch.Write(os.Stdout); err != nil {
				log.Fatal(err)
			}
		} else {
			log.Printf("applying %s", c)
			if err := dst.Apply(patch); err != nil {
				log.Fatalf("%s: apply %s: %s", dst, patch, err)
			}
			if !patch.MaybeContainsLFSPointer() {
				log.Debug.Printf("%s: patch contains no LFS pointers", patch)
				continue
			}
			// Copy any LFS objects that were touched by this change.
			// Doing it this way alllows us to download only LFS objects
			// that actually need to be transferred.
			paths := patch.Paths()
			ptrs, err := dst.ListLFSPointers()
			if err != nil {
				log.Fatal(err)
			}
			for _, ptr := range ptrs {
				if !paths[ptr] {
					continue
				}
				if err := dst.CopyLFSObject(src, ptr); err != nil {
					log.Fatalf("copying LFS object %s: %v", ptr, err)
				}
			}
		}
	}

	if !*push {
		return
	}
	if ncommit == 0 {
		log.Print("nothing to do")
		return
	}
	log.Printf("pushing changes to %s %s", dstURL, dstBranch)
	if err := dst.Push("origin", dstBranch); err != nil {
		log.Fatalf("%s: push origin %s: %v", dst, dstBranch, err)
	}
}

func parseSpec(spec string) (url, prefix, branch string) {
	parts := strings.Split(spec, ",")
	switch len(parts) {
	case 1:
		return parts[0], "", "master"
	case 2:
		return parts[0], parts[1], "master"
	case 3:
		return parts[0], parts[1], parts[2]
	default:
		log.Fatalf("invalid spec %s", spec)
	}
	panic("not reached")
}
