// Copyright 2018 GRAIL, Inc. All rights reserved.
// Use of this source code is governed by the Apache 2.0
// license that can be found in the LICENSE file.

package main

import (
	"flag"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/grailbio/base/log"
	"github.com/grailbio/grit/git"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: grit src dst

Grit copies commits from a source repository to a destination
repository. Src and dst are repository specs of the form:
	url
	url,prefix
	url,prefix,branch
For example, the spec
	ssh://git@mycompany.com/diffusion/REPO/repo.git,myproject/
represents the "myproject" directory in the git repository 
git@mycompany.com/diffusion/REPO/repo.git accessed over SSH.
The default prefix is the empty prefix ("") and the default branch is
"master".

When run, grit checks out the desired repositories in a local cache
(at /var/tmp/grit), and operates directly on these repositories. Commits
are rewritten before they are applied: prefixes are removed as appropriate
and files named BUILD are omitted.

It is not safe to run concurrent invocations of grit on the same machine.
`)
	flag.PrintDefaults()
	os.Exit(2)
}

func main() {
	log.SetPrefix("")
	log.AddFlags()
	dump := flag.Bool("dump", false, "dump patches to stdout instead of applying them to the destination repository")
	push := flag.Bool("push", false, "push applied changes to the destination repository's remote")
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() != 2 {
		flag.Usage()
	}

	srcURL, srcPrefix, srcBranch := parseSpec(flag.Arg(0))
	dstURL, dstPrefix, dstBranch := parseSpec(flag.Arg(1))

	log.Printf("synchronizing repo:%s prefix:%s branch:%s -> repo:%s prefix:%s branch:%s",
		srcURL, srcPrefix, srcBranch, dstURL, dstPrefix, dstBranch)
	if dstPrefix != "" {
		log.Fatal("destination prefixes not yet supported")
	}

	src := git.Open(srcURL, srcPrefix, srcBranch)
	dst := git.Open(dstURL, dstPrefix, dstBranch)

	// Last synchronized commit, if any.
	last := dst.Log("-1", "--grep", `^\(fb\)\?shipit-source-id: [a-z0-9]\+$`)
	var commits []*git.Commit
	if len(last) == 0 {
		log.Printf("performing initial sync")
		commits = src.Log("--no-merges")
	} else {
		log.Printf("synchronizing: last diff: %v, source: %v", last[0].Digest, last[0].ShipitID())
		commits = src.Log(last[0].ShipitID()+"..master", "--ancestry-path", "--no-merges")
	}
	log.Printf("%d commits to copy", len(commits))
	for i := len(commits) - 1; i >= 0; i-- {
		c := commits[i]
		patch := src.Patch(c.Digest)
		if patch.Body != "" {
			patch.Body += "\n\n"
		}
		patch.Body += fmt.Sprintf("fbshipit-source-id: %s", patch.ID.Hex()[:7])
		// Filter out BUILD files and files that begin with "grail_internal".
		// Prefixes are already rewritten by the repo.
		var diffs []git.Diff
		for _, diff := range patch.Diffs {
			base := path.Base(diff.Path)
			if base == "BUILD" || strings.HasPrefix(base, "grail_internal") {
				continue
			}
			diffs = append(diffs, diff)
		}
		if len(diffs) == 0 {
			log.Printf("skipping empty patch %s", patch.ID.Hex()[:7])
			continue
		}
		patch.Diffs = diffs
		if *dump {
			if err := patch.Write(os.Stdout); err != nil {
				log.Fatal(err)
			}
		} else {
			log.Printf("applying %s", c)
			dst.Apply(patch)
		}
	}
	if !*dump && *push {
		log.Printf("pushing changes to %s %s", dstURL, dstBranch)
		dst.Push("origin", dstBranch)
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
