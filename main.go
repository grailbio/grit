// Copyright 2018 GRAIL, Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

// Grit copies commits from a source repository to a destination
// repository. It is intended to mirror projects residing in an
// private monorepo to an external project-specific Git repository.
//
// Usage:
//
// 	grit [-push] [-dump] src dst rules...
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
// Example
//
// Copy commits from the "project/" directory in repository
// ssh://git@git.company.com/foo.git to the root directory in the
// repository https://github.com/company/project.git. Diffs applied
// to files named BUILD are skipped.
//
// 	grit -push ssh://git@git.company.com/foo.git,project/ \
//		https://github.com/company/project.git "strip:^BUILD$" "strip:/BUILD$"
package main

import (
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

func main() {
	log.SetPrefix("")
	log.AddFlags()
	dump := flag.Bool("dump", false, "dump patches to stdout instead of applying them to the destination repository")
	push := flag.Bool("push", false, "push applied changes to the destination repository's remote")
	configs := flag.String("config", "", "comma-separated key-value pairs that should be passed to git")
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
	rules := flag.Args()[2:]
	for _, rule := range rules {
		parts := strings.SplitN(rule, ":", 2)
		if len(parts) != 2 {
			log.Fatalf("invalid rule %s", rule)
		}
		if parts[0] != "strip" {
			log.Fatalf("invalid rule type %s", parts[0])
		}
		r, err := regexp.Compile(parts[1])
		if err != nil {
			log.Fatalf("invalid regexp %s: %s", parts[1], err)
		}
		strip = append(strip, r)
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

	// Last synchronized commit, if any.
	last, err := dst.Log("-1", "--grep", `^\(fb\)\?shipit-source-id: [a-z0-9]\+$`)
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
		commits, err = src.Log(last[0].ShipitID()+"..master", "--ancestry-path", "--no-merges")
		if err != nil {
			log.Fatalf("log %s: %v", src, err)
		}
	}
	// Filter out commits which are themselves copies, so that
	// we can properly support multi-way syncing.
	raw := commits
	commits = nil
	for _, commit := range raw {
		if commit.ShipitID() == "" {
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
			paths := patch.Paths()

			// Copy any LFS objects that were touched by this change.
			// Doing it this way alllows us to download only LFS objects
			// that actually need to be transferred.
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
