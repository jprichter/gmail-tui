export default {
  baseBranch: 'main',
  branchPrefix: 'feat/',
  remote: 'origin',
  engines: {
    default: { name: 'codex', model: 'gpt-6.1-sol', effort: 'medium' },
    review: { name: 'codex', model: 'gpt-6-luna', effort: 'high' },
    docs: { name: 'claude', model: 'claude-haiku-5-5', effort: 'high' },
    'pr-description': { name: 'claude', model: 'claude-haiku-5-5', effort: 'high' },
  },
  phases: ['implement', 'docs', 'gate', 'review', 'address', 'pr-description', 'publish'],
  publish: { backend: 'github', draft: true },
  gate: [
    "rtk proxy env GOMODCACHE=/tmp/chat-security-tools/mod GOCACHE=/tmp/chat-security-tools/build GOTOOLCHAIN=auto sh -c 'go test -race ./...'",
    "rtk proxy env GOMODCACHE=/tmp/chat-security-tools/mod GOCACHE=/tmp/chat-security-tools/build GOTOOLCHAIN=auto sh -c 'go vet ./...'",
    // Explicit Git context keeps VCS stamping correct in linked worktrees.
    `rtk proxy env GOMODCACHE=/tmp/chat-security-tools/mod GOCACHE=/tmp/chat-security-tools/build GOTOOLCHAIN=auto sh -c 'GIT_DIR="$(rtk proxy git rev-parse --absolute-git-dir)" GIT_WORK_TREE="$(rtk proxy git rev-parse --show-toplevel)" go build ./...'`,
  ],
  maxRounds: 5,
  timeoutMs: 60 * 60 * 1000,
  worktrees: true,
};
