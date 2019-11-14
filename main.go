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
//  strip:regexp
//    Strips diffs applied to files matching the given regular
//    expression.
//
//  strip-message:regexp
//    Strips commit messages when all files with changes match the given
//    regular expression. This rule can be used to push internal cross-repo
//    maintenance changes that do not need a context in the external world. For
//    example, go.mod and go.sum files.
//
//  strip-commit:hash
//    Strip the commit named by the given hash. This is useful for excluding
//    troublesome commits that you know are safe to ignore.
//
//  rewrite:regexp:/old_re/new_re/
//    For each file whose path matches regexp, regexp-replace each line in the
//    file from old_re to new_re. For example, rule
//
//  rewrite:go.mod$:/replace .* => .*//
//    will remove all "replace from => to" directives from go.mod
//    files.  The 2nd letter after the path regexp ('/' in the example)
//    determines the separator character for the old and the new regexps. The
//    previous example can also be written as
//
//  rewrite:go.mod$:!replace .* => .*!!
//
// One way sync
//
// Copy commits from the "project/" directory in repository
// ssh://git@git.company.com/foo.git to the root directory in the
// repository https://github.com/company/project.git. Diffs applied
// to files named BUILD are skipped.
//
// 	grit -push ssh://git@git.company.com/foo.git,project/ \
//		https://github.com/company/project.git "strip:^BUILD$" "strip:/BUILD$"
//
// Two-way sync
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
//  cd /var/tmp/grit/grailXXXXX
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

	var rules rules
	for _, rule := range flag.Args()[2:] {
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
			rules.strip = append(rules.strip, r)
		case "strip-message":
			r, err := regexp.Compile(parts[1])
			if err != nil {
				log.Fatalf("invalid regexp %s: %s", parts[1], err)
			}
			rules.stripMessagePaths = append(rules.stripMessagePaths, r)
		case "strip-commit":
			hash := parts[1]
			if len(hash) < 7 {
				log.Fatalf("invalid commit prefix %s: must have at least 7 digits", parts[1])
			}
			for _, d := range hash {
				if (d < '0' || d > '9') && (d < 'a' || d > 'f') && (d < 'A' || d > 'F') {
					log.Fatalf("invalid commit prefix %s: invalid hex digit %c", hash, d)
				}
			}
			rules.stripCommits = append(rules.stripCommits, hash)
		case "rewrite":
			rules.rewrite = append(rules.rewrite, parseRewriteRule(parts[1]))
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

	// Last synchronized commit that applies, if any. We apply the
	// rewrite rules here, so that we skip commits that may be tagged
	// with shipit IDs, but wouldn't actually come from the source
	// repository. This can happen if a repository is the destination
	// for multiple repositories, and commits sourced from one repo can
	// touch those in another. A common source of this is Bazel BUILD
	// files and go.{mod,sum} files that may be modified independently
	// in the source and destination repositories.
	var lastCommit *git.Commit
	for head := "HEAD"; ; {
		last, err := dst.Log("-1", "--grep", `^\s*\(fb\)\?shipit-source-id: [a-z0-9]\+$`, head)
		if err != nil {
			log.Fatalf("log %s: %v", dst, err)
		}
		if len(last) == 0 {
			break
		}
		applies, err := rules.isCommitApplicable(last[0], dst)
		if err != nil {
			log.Fatalf("isCommitApplicable %s: %v", last[0], err)
		}
		if applies {
			lastCommit = last[0]
			break
		}
		log.Printf("commit %s is not applicable to %s: skipping", last[0], dst)
		head = last[0].Digest.Hex() + "^"
	}
	var commits []*git.Commit
	if lastCommit == nil {
		log.Printf("performing initial sync")
		var err error
		commits, err = src.Log("--no-merges")
		if err != nil {
			log.Fatalf("log %s: %v", src, err)
		}
	} else {
		log.Printf("synchronizing: last diff: %v, source: %v", lastCommit.Digest, lastCommit.ShipitID())
		ids := lastCommit.ShipitID()
		if len(ids) == 0 {
			log.Fatalf("no fbshipit-source-id found in commit: %+v", lastCommit)
		}
		// When a commit is a squash of multiple commits, they are sorted in
		// ascending chronological order. So the last ID is the one we should sync
		// from.
		newestID := ids[len(ids)-1]
		var err error
		commits, err = src.Log(newestID+"..master", "--ancestry-path", "--no-merges")
		if err != nil {
			log.Fatalf("log %s: %v", src, err)
		}
	}

	// Filter out commits which are themselves copies, so that
	// we can properly support multi-way syncing.
	// We also filter out commits that match any stripped commits.
	raw := commits
	commits = nil
commitsLoop:
	for _, commit := range raw {
		if len(commit.ShipitID()) > 0 {
			continue
		}
		if rules.isStripped(commit) {
			log.Debug.Printf("commit %s: stripped by strip-commit rule", commit.Digest)
			continue commitsLoop
		}
		commits = append(commits, commit)
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
		shipitTag := fmt.Sprintf("fbshipit-source-id: %s", patch.ID.Hex()[:7])
		patch.Body += shipitTag
		// Apply filepath specific rules.
		// Prefixes are already rewritten by the repo.
		var diffs []git.Diff
		stripMessage := true
	diffloop:
		for _, diff := range patch.Diffs {
			if match, re := rules.isPathStripped(diff.Path); match {
				log.Debug.Printf("file %s matches rule %s: stripping", diff.Path, re)
				continue diffloop
			}
			if match, re := rules.isMessagePathStripped(diff.Path); match {
				log.Debug.Printf("file %s matches rule %s for stripping commit messages", diff.Path, re)
			} else {
				stripMessage = false
			}
			rules.rewriteDiff(&diff)
			diffs = append(diffs, diff)
		}
		if len(diffs) == 0 {
			log.Printf("skipping empty patch %s", patch.ID.Hex()[:7])
			continue
		}
		ncommit++
		patch.Diffs = diffs
		if stripMessage {
			patch.Subject = "Stripped commit"
			patch.Body = "Commit message stripped.\n\n" + shipitTag
		}
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
			// Doing it this way allows us to download only LFS objects
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

type rules struct {
	strip             []*regexp.Regexp
	stripMessagePaths []*regexp.Regexp
	// We store strip prefixes as strings since digesters refuse
	// to parse odd-length hex strings and git typically gives out
	// a prefix with 7 digits.
	stripCommits []string
	rewrite      []rewriteRule
}

// isStripped returns whether this commit matches the strip rules of
// the rule set r.
func (r rules) isStripped(c *git.Commit) bool {
	for _, stripped := range r.stripCommits {
		if strings.HasPrefix(c.Digest.Hex(), stripped) {
			return true
		}
	}
	return false
}

// isPathStripped returns whether the provided path is stripped by the
// ruleset's strip path rules.
func (r rules) isPathStripped(path string) (bool, *regexp.Regexp) {
	for _, re := range r.strip {
		if re.MatchString(path) {
			return true, re
		}
	}
	return false, nil
}

// isMessagePathStripped returns whether the provided path is stripped
// by the ruleset's message strip rules.
func (r rules) isMessagePathStripped(path string) (bool, *regexp.Regexp) {
	for _, re := range r.stripMessagePaths {
		if re.MatchString(path) {
			return true, re
		}
	}
	return false, nil
}

// rewriteDiff applies the rulesets rewrite rules to the provided diff.
func (r rules) rewriteDiff(diff *git.Diff) {
	for _, r := range r.rewrite {
		if r.pathRe.MatchString(diff.Path) {
			diff.Body = r.rewrite(diff.Body)
		}
	}
}

// isCommitApplicable returns whether the provided commit is non-empty
// in the provided repository and prefix.
func (r rules) isCommitApplicable(c *git.Commit, src *git.Repo) (bool, error) {
	if r.isStripped(c) {
		return false, nil
	}
	patch, err := src.Patch(c.Digest, "")
	if err != nil {
		return false, err
	}
	var ndiff int
	for _, diff := range patch.Diffs {
		if match, _ := r.isPathStripped(diff.Path); match {
			continue
		}
		ndiff++
	}
	return ndiff > 0, nil
}

/*
func isApplicableCommit(c *git.Commit, stripCommits []string) bool {
	for _, stripped := range stripCommits {
			if strings.HasPrefix(c.Digest.Hex(), stripped) {
				return false
			}
	}
	patch, err :=

}
*/
