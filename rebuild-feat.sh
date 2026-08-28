#!/usr/bin/env bash
#
# rebuild-feat.sh
#
# Rebuild the fork's integration branch from scratch:
#
#   <upstream>/<base>                      (origin/dev)
#   + one --no-ff merge per open PR        (your open PRs against <base>, from the fork)
#   + one --no-ff merge per extra PR       (other authors' PRs listed in EXTRA_PRS)
#   + the branch's own commits             (its non-merge first-parent commits)
#
# Each PR branch is the single source of truth: fix a PR on its own branch,
# push it, then re-run this script. A PR that merges upstream drops out of the
# list by itself.
#
# Conflicts between PRs are resolved once and replayed by git rerere (stored in
# .git/rr-cache). On a conflict rerere cannot resolve, or a failing test, the
# script stops and leaves its worktree in place: resolve there, commit, and
# re-run; rerere then replays your resolution.
#
# The rebuild runs in a temporary worktree; the branch itself only moves at the
# end. It refuses to overwrite local commits that the fork does not have.
#
# go build and go vet must pass. If go test fails, only the failed packages are
# re-run, once: the rebuild goes on if they pass, and names the tests that passed
# only on retry (likely flaky; worth a look if the same one keeps appearing).
#
# Each run appends one line to $(git rev-parse --git-common-dir)/rebuild-feat-history.log
# (never committed): time, mode, result, base and rebuilt commits, and the tests
# that needed a retry or why the run stopped. To see how often runs stop or retry:
#   cat "$(git rev-parse --git-common-dir)/rebuild-feat-history.log"
#
# Extra PRs are other authors' open PRs the branch should carry until they
# merge (for example a fix it depends on). Each is fetched from the upstream's
# refs/pull/<n>/head and pinned to the commit you reviewed: if the author
# pushes new commits, the rebuild stops until you review them and update the
# pin, so unreviewed code never lands in the branch. A merged extra PR is
# skipped (the base carries it); a closed or draft one, or one against another
# base, is skipped with a warning.
#
# Don't `git rebase` this branch onto a newer base: that flattens the PR merges
# back into a long list of copied commits. Re-run this script instead. To change
# one of the branch's own commits, commit or amend it on the branch, push, then
# re-run.
#
# Usage:
#   ./rebuild-feat.sh [options]
#
# Options:
#   --push           Move the local branch to the rebuild and force-push it to
#                    the fork, with a lease on the fork's branch as it was when
#                    the rebuild started. Without it, the run is a dry run: it
#                    builds, tests and reports, and leaves the branch alone.
#   --skip-tests     Skip go build / go vet / go test.
#   --keep-worktree  Keep the temporary worktree after a successful run.
#   -h, --help       Show this help.
#
# Env overrides:
#   UPSTREAM_REMOTE  Upstream remote (default: origin).
#   BASE_BRANCH      Upstream base branch (default: dev).
#   FORK_REMOTE      Fork remote holding the PR branches (default: KennethWKZ).
#   FEAT_BRANCH      Branch to rebuild (default: feat/claude-oauth-base-url).
#   GH_REPO          Upstream GitHub repo (default: router-for-me/CLIProxyAPI).
#   PR_AUTHOR        GitHub login whose open PRs are merged (default: KennethWKZ).
#   EXCLUDE_PRS      Space-separated PR numbers to leave out (default: none).
#   EXTRA_PRS        Space-separated other authors' PRs to merge, each <number>@<sha>
#                    (the reviewed head; a prefix of 7+ hex digits). Default:
#                    none. Set to "" to merge none.
#   INCLUDE_DRAFTS   1 to include draft PRs (default: 0).

set -euo pipefail

# Run from a private copy: rebuilding may rewrite this file in the checkout.
if [[ -z "${REBUILD_FEAT_REEXEC:-}" ]]; then
	REBUILD_FEAT_REPO="$(cd "$(dirname "$0")" && git rev-parse --show-toplevel)"
	export REBUILD_FEAT_REPO
	script_copy="$(mktemp "${TMPDIR:-/tmp}/rebuild-feat.XXXXXX")"
	cp "$0" "$script_copy"
	REBUILD_FEAT_REEXEC=1 exec bash "$script_copy" "$@"
fi
trap 'rm -f "$0"' EXIT

UPSTREAM_REMOTE="${UPSTREAM_REMOTE:-origin}"
BASE_BRANCH="${BASE_BRANCH:-dev}"
FORK_REMOTE="${FORK_REMOTE:-KennethWKZ}"
FEAT_BRANCH="${FEAT_BRANCH:-feat/claude-oauth-base-url}"
GH_REPO="${GH_REPO:-router-for-me/CLIProxyAPI}"
PR_AUTHOR="${PR_AUTHOR:-KennethWKZ}"
EXCLUDE_PRS="${EXCLUDE_PRS:-}"
# An explicit empty EXTRA_PRS disables the default.
EXTRA_PRS="${EXTRA_PRS-}"
INCLUDE_DRAFTS="${INCLUDE_DRAFTS:-0}"

DO_PUSH=0
SKIP_TESTS=0
KEEP_WORKTREE=0

usage() { sed -n '2,/^$/s/^# \{0,1\}//p' "$0"; }

while [[ $# -gt 0 ]]; do
	case "$1" in
	--push) DO_PUSH=1 ;;
	--skip-tests) SKIP_TESTS=1 ;;
	--keep-worktree) KEEP_WORKTREE=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "unknown option: $1" >&2
		usage >&2
		exit 2
		;;
	esac
	shift
done

log() { printf '==> %s\n' "$*"; }

# record appends this run's outcome to the history log; it never fails the run.
history_file=""
run_mode="dry-run"
[[ $DO_PUSH -eq 1 ]] && run_mode="push"
base_sha="-"
new_sha="-"
retried=""
record() {
	[[ -n "$history_file" ]] || return 0
	printf '%s\t%s\t%s\tbase=%s\tfeat=%s\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$run_mode" "$1" \
		"${base_sha:0:9}" "${new_sha:0:9}" "${2:-}" >>"$history_file" 2>/dev/null || true
}
die() {
	printf 'error: %s\n' "$*" >&2
	record stopped "$*"
	exit 1
}

cd "$REBUILD_FEAT_REPO"
history_file="$(git rev-parse --path-format=absolute --git-common-dir)/rebuild-feat-history.log"
command -v gh >/dev/null || die "gh is required to list the open PRs"
command -v go >/dev/null || [[ $SKIP_TESTS -eq 1 ]] || die "go is required for the tests (or pass --skip-tests)"

base_ref="$UPSTREAM_REMOTE/$BASE_BRANCH"
fork_feat_ref="refs/remotes/$FORK_REMOTE/$FEAT_BRANCH"

log "fetching $UPSTREAM_REMOTE and $FORK_REMOTE"
git fetch --quiet --prune "$UPSTREAM_REMOTE"
git fetch --quiet --prune "$FORK_REMOTE"

git rev-parse --verify --quiet "$fork_feat_ref" >/dev/null || die "$FORK_REMOTE/$FEAT_BRANCH not found"
old_fork_sha="$(git rev-parse "$fork_feat_ref")"
base_sha="$(git rev-parse "$base_ref")"

# Local commits the fork does not have would be lost when the branch moves.
if git rev-parse --verify --quiet "refs/heads/$FEAT_BRANCH" >/dev/null; then
	unpushed="$(git rev-list --count "$fork_feat_ref..refs/heads/$FEAT_BRANCH")"
	[[ "$unpushed" -eq 0 ]] || die "local $FEAT_BRANCH has $unpushed commit(s) $FORK_REMOTE does not have; push or drop them first"
fi

excluded() {
	local n
	for n in $EXCLUDE_PRS; do
		[[ "$n" == "$1" ]] && return 0
	done
	return 1
}

# Open PRs to merge, oldest first: number<TAB>ref<TAB>label<TAB>title.
draft_filter='select(.isDraft | not)'
[[ "$INCLUDE_DRAFTS" == "1" ]] && draft_filter='.'
own_prs="$(gh pr list -R "$GH_REPO" --author "$PR_AUTHOR" --state open --base "$BASE_BRANCH" --limit 200 \
	--json number,headRefName,title,isDraft,headRepositoryOwner \
	--jq ".[] | $draft_filter | select(.headRepositoryOwner.login == \"$PR_AUTHOR\") | \"\(.number)\t\(.headRefName)\t\(.title)\"" |
	sort -n)"
prs=""
while IFS=$'\t' read -r number branch title; do
	[[ -n "$number" ]] || continue
	excluded "$number" && continue
	ref="refs/remotes/$FORK_REMOTE/$branch"
	git rev-parse --verify --quiet "$ref" >/dev/null || die "PR #$number branch $FORK_REMOTE/$branch not found"
	prs+="$number"$'\t'"$ref"$'\t'"$branch"$'\t'"$title"$'\n'
done <<<"$own_prs"

# Extra PRs by other authors, each pinned to a reviewed head.
for entry in $EXTRA_PRS; do
	number="${entry%%@*}"
	pin="${entry#*@}"
	[[ "$number" =~ ^[0-9]+$ && "$entry" == *@* && "$pin" =~ ^[0-9a-f]{7,40}$ ]] ||
		die "EXTRA_PRS entry \"$entry\" is not <number>@<sha> (7+ hex digits)"
	excluded "$number" && continue
	info="$(gh pr view "$number" -R "$GH_REPO" --json state,isDraft,baseRefName,headRefOid,headRefName,title,author \
		--jq '[.state, (.isDraft | tostring), .baseRefName, .headRefOid, .author.login, .headRefName, .title] | join("\t")')" ||
		die "cannot read PR #$number from $GH_REPO"
	IFS=$'\t' read -r state draft base head author branch title <<<"$info"
	case "$state" in
	MERGED)
		log "extra PR #$number is merged upstream; skipping it ($base_ref carries it once it includes the merge)"
		continue
		;;
	OPEN) ;;
	*)
		log "warning: extra PR #$number is $state; skipping it (drop it from EXTRA_PRS)"
		continue
		;;
	esac
	if [[ "$draft" == "true" && "$INCLUDE_DRAFTS" != "1" ]]; then
		log "warning: extra PR #$number is a draft; skipping it (INCLUDE_DRAFTS=1 to merge drafts)"
		continue
	fi
	if [[ "$base" != "$BASE_BRANCH" ]]; then
		log "warning: extra PR #$number targets $base, not $BASE_BRANCH; skipping it"
		continue
	fi
	if [[ "$head" != "$pin"* ]]; then
		die "extra PR #$number moved: head is ${head:0:9}, pinned ${pin}. Review the new commits (gh pr diff $number -R $GH_REPO), then set EXTRA_PRS to $number@${head:0:9}"
	fi
	ref="refs/rebuild-feat/pr/$number"
	git fetch --quiet "$UPSTREAM_REMOTE" "+refs/pull/$number/head:$ref" || die "cannot fetch PR #$number from $UPSTREAM_REMOTE"
	[[ "$(git rev-parse "$ref")" == "$head" ]] ||
		die "fetched PR #$number at $(git rev-parse --short "$ref"), but GitHub reports ${head:0:9}; re-run"
	prs+="$number"$'\t'"$ref"$'\t'"$author:$branch"$'\t'"$title"$'\n'
done
prs="${prs%$'\n'}"
[[ -n "$prs" ]] || die "no open PRs by $PR_AUTHOR against $BASE_BRANCH, and no extra PRs"

# Own commits: the fork branch's non-merge first-parent commits on top of base.
own="$(git rev-list --reverse --first-parent --no-merges "$base_ref..$fork_feat_ref")"

# Guard: an own commit that matches a PR commit means the branch still carries
# PR history as plain commits (not merges), and would apply it twice.
pr_subjects="$(printf '%s\n' "$prs" | while IFS=$'\t' read -r _ ref _ _; do
	git log --format='%s' "$base_ref..$ref"
done)"
for commit in $own; do
	subject="$(git log -1 --format='%s' "$commit")"
	if printf '%s\n' "$pr_subjects" | grep -qxF -- "$subject"; then
		die "own commit $(git rev-parse --short "$commit") \"$subject\" matches an open PR's commit; $FEAT_BRANCH looks like a linear copy of PR history, not the merge layout"
	fi
done

log "base: $base_ref $(git rev-parse --short "$base_ref")"
log "PRs to merge:"
printf '%s\n' "$prs" | while IFS=$'\t' read -r number ref label title; do
	printf '      #%s %s %s  %s\n' "$number" "$label" "$(git rev-parse --short "$ref")" "$title"
done
log "own commits to apply:"
for commit in $own; do
	printf '      %s\n' "$(git log -1 --format='%h %s' "$commit")"
done

worktree="$(mktemp -d "${TMPDIR:-/tmp}/rebuild-feat-wt.XXXXXX")"
git worktree add --quiet --detach "$worktree" "$base_ref"
cleanup_worktree() { git -C "$REBUILD_FEAT_REPO" worktree remove --force "$worktree"; }
stop() {
	printf 'error: %s\n' "$*" >&2
	record stopped "$*"
	printf '       worktree kept for you: %s\n' "$worktree" >&2
	printf '       resolve there, commit, then re-run this script (rerere replays the resolution);\n' >&2
	printf '       remove it with: git worktree remove --force %s\n' "$worktree" >&2
	exit 1
}

g() { git -C "$worktree" -c rerere.enabled=true -c rerere.autoupdate=true "$@"; }
unmerged() { g diff --name-only --diff-filter=U; }

# A merge or cherry-pick that stopped on conflicts rerere fully resolved is
# committed as is; anything left unresolved stops the rebuild.
finish_or_stop() {
	local what="$1" continue_cmd="$2"
	if [[ -n "$(unmerged)" ]]; then
		printf '%s\n' "$(unmerged)" | sed 's/^/      unresolved: /' >&2
		stop "conflict in $what that rerere has no resolution for"
	fi
	log "  rerere resolved the conflict in $what"
	case "$continue_cmd" in
	merge) g commit --quiet --no-edit --cleanup=strip ;;
	cherry-pick) GIT_EDITOR=true g cherry-pick --continue >/dev/null ;;
	esac
}

while IFS=$'\t' read -r number ref label title; do
	log "merging #$number ($label)"
	if ! g merge --quiet --no-ff "$ref" \
		-m "Merge #$number ($label) into $FEAT_BRANCH" -m "$title" >/dev/null 2>&1; then
		finish_or_stop "#$number ($label)" merge
	fi
done <<<"$prs"

for commit in $own; do
	log "applying $(git log -1 --format='%h %s' "$commit")"
	if ! g cherry-pick --allow-empty "$commit" >/dev/null 2>&1; then
		finish_or_stop "$(git log -1 --format='%h' "$commit")" cherry-pick
	fi
done

new_sha="$(g rev-parse HEAD)"
log "rebuilt $FEAT_BRANCH at $(git rev-parse --short "$new_sha"); changes against $FORK_REMOTE/$FEAT_BRANCH:"
git diff --stat "$old_fork_sha" "$new_sha" | tail -n 1 | sed 's/^/      /'
if git diff --quiet "$old_fork_sha" "$new_sha"; then
	log "  tree identical to $FORK_REMOTE/$FEAT_BRANCH"
fi
old_base="$(git merge-base "$old_fork_sha" "$base_ref")"
if [[ "$old_base" != "$(git rev-parse "$base_ref")" ]]; then
	log "  $base_ref moved by $(git rev-list --count "$old_base..$base_ref") commit(s) since the last build; part of the change is theirs:"
	git diff --stat "$old_base" "$base_ref" | tail -n 1 | sed 's/^/      /'
fi

if [[ $SKIP_TESTS -eq 0 ]]; then
	log "go build ./... && go vet ./..."
	(cd "$worktree" && go build ./... && go vet ./...) || stop "go build or go vet failed"
	log "go test ./..."
	test_log="$worktree/.rebuild-feat-test.log"
	if ! (cd "$worktree" && go test ./... 2>&1 | tee "$test_log"); then
		failed_pkgs="$(awk '$1 == "FAIL" && $2 != "" { print $2 }' "$test_log" | sort -u | tr '\n' ' ')"
		failed_pkgs="${failed_pkgs% }"
		failed_tests="$(sed -nE 's/^--- FAIL: ([^ ]+).*/\1/p' "$test_log" | sort -u | tr '\n' ' ')"
		failed_tests="${failed_tests% }"
		[[ -n "$failed_pkgs" ]] || stop "go test failed"
		log "retrying the failed package(s) once: $failed_pkgs"
		# Word splitting is intended: one package per word.
		# shellcheck disable=SC2086
		(cd "$worktree" && go test -count=1 $failed_pkgs) ||
			stop "go test failed again on retry: ${failed_tests:-$failed_pkgs}"
		retried="${failed_tests:-$failed_pkgs}"
		log "passed on retry (likely flaky; worth a look if it keeps appearing): $retried"
	fi
fi

if [[ $DO_PUSH -eq 0 ]]; then
	log "dry run: $FEAT_BRANCH left unchanged; re-run with --push to move and publish it"
	record ok "${retried:+retried: $retried}"
	if [[ $KEEP_WORKTREE -eq 1 ]]; then
		log "worktree kept: $worktree"
	else
		cleanup_worktree
	fi
	exit 0
fi

# Move the branch. Where it is checked out, the checkout must be clean.
checked_out_at="$(git worktree list --porcelain | awk -v ref="branch refs/heads/$FEAT_BRANCH" '
	/^worktree / { path = substr($0, 10) }
	$0 == ref { print path }')"
if [[ -n "$checked_out_at" ]]; then
	[[ -z "$(git -C "$checked_out_at" status --porcelain)" ]] ||
		stop "$FEAT_BRANCH is checked out at $checked_out_at with uncommitted changes"
	git -C "$checked_out_at" reset --quiet --keep "$new_sha"
else
	git branch --force "$FEAT_BRANCH" "$new_sha"
fi
log "local $FEAT_BRANCH now at $(git rev-parse --short "$new_sha")"

log "pushing to $FORK_REMOTE (lease: $(git rev-parse --short "$old_fork_sha"))"
git push --force-with-lease="$FEAT_BRANCH:$old_fork_sha" "$FORK_REMOTE" "$FEAT_BRANCH" ||
	stop "push to $FORK_REMOTE failed (the lease, or the network); local $FEAT_BRANCH is already at $(git rev-parse --short "$new_sha")"
record pushed "${retried:+retried: $retried}"

if [[ $KEEP_WORKTREE -eq 1 ]]; then
	log "worktree kept: $worktree"
else
	cleanup_worktree
fi
