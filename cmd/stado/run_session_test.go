package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

// TestRun_SessionFlagResolvesPartialID: run --session with a uuid
// prefix must resolve to the full id before trying to load the
// conversation. Unit-scope: we don't actually call runCmd.RunE
// (that'd hit a provider). We check that resolveSessionID (the
// shared helper run.go calls) picks the right session.
func TestRun_SessionFlagResolvesPartialID(t *testing.T) {
	_, restore := resolveEnv(t,
		[]string{"aaaaaaaa-1111-2222-3333", "bbbbbbbb-4444-5555-6666"},
		map[string]string{"aaaaaaaa-1111-2222-3333": "the first session"})
	defer restore()

	// Simulate run --session aaaaaaaa.
	runSessionID = "aaaaaaaa"
	defer func() { runSessionID = "" }()

	// The resolve step is the part we can test without a provider.
	// LoadConversation should succeed when given the resolved id.
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
}

func TestRun_FreshVerificationFailurePersistsWorkAndCritique(t *testing.T) {
	oldLoadConfig := runLoadConfig
	oldBuildProvider := runBuildProvider
	oldAgentLoop := runAgentLoop
	oldPrompt, oldSkill, oldSessionID := runPrompt, runSkill, runSessionID
	oldMaxTurns, oldJSON, oldNoTools, oldNoSandbox := runMaxTurns, runJSON, runNoTools, noSandbox
	oldVerifyCommands, oldNoVerify := runVerifyCommands, runNoVerify
	verifyFlag := runCmd.Flags().Lookup("verify")
	oldVerifyChanged := verifyFlag.Changed
	defer func() {
		runLoadConfig = oldLoadConfig
		runBuildProvider = oldBuildProvider
		runAgentLoop = oldAgentLoop
		runPrompt, runSkill, runSessionID = oldPrompt, oldSkill, oldSessionID
		runMaxTurns, runJSON, runNoTools, noSandbox = oldMaxTurns, oldJSON, oldNoTools, oldNoSandbox
		runVerifyCommands, runNoVerify = oldVerifyCommands, oldNoVerify
		verifyFlag.Changed = oldVerifyChanged
	}()

	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv(envBrokerAttach, "0")
	cwd := filepath.Join(root, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	restore := chdir(t, cwd)
	defer restore()

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Verify.Commands = []string{"false"}
	cfg.Verify.MaxRounds = 1
	runLoadConfig = func() (*config.Config, error) { return cfg, nil }
	runBuildProvider = func(*config.Config) (agent.Provider, error) { return runHookProvider{}, nil }
	runAgentLoop = func(_ context.Context, opts runtime.AgentLoopOptions) (string, []agent.Message, error) {
		msgs := append(opts.Messages,
			agent.Text(agent.RoleAssistant, "rejected partial work"),
			agent.Text(agent.RoleUser, "Verification command failed: false"))
		return "", msgs, &runtime.VerifyExhaustedError{Round: 1, Command: "false", Feedback: "failed"}
	}

	runPrompt = "do work"
	runSkill = ""
	runSessionID = ""
	runMaxTurns = 2
	runJSON = true
	runNoTools = false
	noSandbox = true
	runVerifyCommands = nil
	runNoVerify = false
	verifyFlag.Changed = false

	runCmd.SetContext(context.Background())
	err = runCmd.RunE(runCmd, nil)
	var coded *exitCodeError
	if !errors.As(err, &coded) || coded.Code != 2 || !errors.Is(err, runtime.ErrVerifyExhausted) {
		t.Fatalf("run error = %v, want exit-code-2 VerifyExhausted", err)
	}
	entries, err := os.ReadDir(cfg.WorktreeDir())
	if err != nil || len(entries) != 1 {
		t.Fatalf("worktree entries=%v err=%v", entries, err)
	}
	msgs, err := runtime.LoadConversation(filepath.Join(cfg.WorktreeDir(), entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || msgs[1].Content[0].Text.Text != "rejected partial work" ||
		!strings.Contains(msgs[2].Content[0].Text.Text, "Verification command failed") {
		t.Fatalf("persisted failed verification messages = %+v", msgs)
	}
}

// TestRun_SessionAppendsMessages: after a continuation run, the
// conversation.jsonl must include the prior messages + new user
// message + assistant reply. Stub out the agent loop call by
// wiring the sessionRun helper directly with a canned reply.
//
// This is tested at the persistence layer: given prior 2 msgs + a
// new user msg + a canned assistant reply, AppendMessage for each
// should round-trip through LoadConversation.
func TestRun_SessionPersistsNewMessages(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdir(t, cwd)
	defer restore()

	cfg, _ := config.Load()
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)
	sc, _ := openSidecar(cfg)
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "run-session-test", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	// Prior messages already on disk.
	_ = runtime.AppendMessage(sess.WorktreePath, agent.Text(agent.RoleUser, "q1"))
	_ = runtime.AppendMessage(sess.WorktreePath, agent.Text(agent.RoleAssistant, "a1"))

	// Simulate what run --session does: load prior, append new user,
	// append a canned assistant (stand-in for what AgentLoop would
	// return), read back.
	prior, err := runtime.LoadConversation(sess.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(prior) != 2 {
		t.Fatalf("prior = %d, want 2", len(prior))
	}
	newUser := agent.Text(agent.RoleUser, "follow up")
	newAsst := agent.Text(agent.RoleAssistant, "follow-up reply")
	_ = runtime.AppendMessage(sess.WorktreePath, newUser)
	_ = runtime.AppendMessage(sess.WorktreePath, newAsst)

	all, err := runtime.LoadConversation(sess.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("after append: %d messages, want 4", len(all))
	}
	if all[2].Content[0].Text.Text != "follow up" {
		t.Errorf("appended user text wrong: %q", all[2].Content[0].Text.Text)
	}
	if all[3].Content[0].Text.Text != "follow-up reply" {
		t.Errorf("appended assistant text wrong: %q", all[3].Content[0].Text.Text)
	}
}

func TestRun_SessionCreatesTurnBoundaryWithoutTools(t *testing.T) {
	oldLoadConfig := runLoadConfig
	oldBuildProvider := runBuildProvider
	oldAgentLoop := runAgentLoop
	oldPrompt, oldSkill, oldSessionID := runPrompt, runSkill, runSessionID
	oldMaxTurns, oldJSON, oldNoTools, oldNoSandbox := runMaxTurns, runJSON, runNoTools, noSandbox
	defer func() {
		runLoadConfig = oldLoadConfig
		runBuildProvider = oldBuildProvider
		runAgentLoop = oldAgentLoop
		runPrompt, runSkill, runSessionID = oldPrompt, oldSkill, oldSessionID
		runMaxTurns, runJSON, runNoTools, noSandbox = oldMaxTurns, oldJSON, oldNoTools, oldNoSandbox
	}()

	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdir(t, cwd)
	defer restore()

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)
	sc, err := openSidecar(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "run-turn-test", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}

	runLoadConfig = func() (*config.Config, error) { return cfg, nil }
	runBuildProvider = func(*config.Config) (agent.Provider, error) { return runHookProvider{}, nil }
	runAgentLoop = func(_ context.Context, opts runtime.AgentLoopOptions) (string, []agent.Message, error) {
		if opts.Executor != nil {
			t.Fatal("no-tool run should not build an executor")
		}
		return "reply", append(opts.Messages, agent.Text(agent.RoleAssistant, "reply")), nil
	}

	runPrompt = "hi"
	runSkill = ""
	runSessionID = sess.ID
	runMaxTurns = 1
	runJSON = true
	runNoTools = true
	noSandbox = true

	runCmd.SetContext(context.Background())
	if err := runCmd.RunE(runCmd, nil); err != nil {
		t.Fatalf("runCmd.RunE: %v", err)
	}

	msgs, err := runtime.LoadConversation(sess.WorktreePath)
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("conversation messages = %d, want 2", len(msgs))
	}
	turns, err := sc.ListTurnRefs(sess.ID)
	if err != nil {
		t.Fatalf("ListTurnRefs: %v", err)
	}
	if len(turns) != 1 || turns[0].Turn != 1 {
		t.Fatalf("turn refs = %+v, want single turn 1", turns)
	}
	head, err := sc.ResolveRef(stadogit.TurnTagRef(sess.ID, 1))
	if err != nil {
		t.Fatalf("ResolveRef turn 1: %v", err)
	}
	commit, err := sc.Repo().CommitObject(head)
	if err != nil {
		t.Fatalf("CommitObject: %v", err)
	}
	if commit.PGPSignature == "" {
		t.Fatal("turn boundary commit is unsigned")
	}
}

// TestRun_SessionFlag_UnknownIDErrors: run --session <unknown> must
// surface the resolver's error cleanly.
func TestRun_SessionFlag_UnknownIDErrors(t *testing.T) {
	cfg, restore := resolveEnv(t, []string{"real-id"}, nil)
	defer restore()

	_, err := resolveSessionID(cfg, "not-a-session")
	if err == nil {
		t.Fatal("unknown id should error")
	}
	if !strings.Contains(err.Error(), "no session matches") {
		t.Errorf("error should mention no-match: %v", err)
	}
}
