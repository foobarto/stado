package main

import (
	"fmt"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/sessioncontext"
	"github.com/spf13/cobra"
	"path/filepath"
)

func withSessionContext(cmd *cobra.Command, fn func(*sessioncontext.Service) error) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	store, err := wal.OpenShared(filepath.Join(cfg.StateDir(), "broker", "events"))
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	return fn(sessioncontext.New(store))
}

var sessionStateCmd = &cobra.Command{Use: "state <session-id>", Short: "Show the bounded structured session state", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return withSessionContext(cmd, func(s *sessioncontext.Service) error {
		state, err := s.State(args[0])
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), state)
	})
}}
var sessionSignalsCmd = &cobra.Command{Use: "signals <session-id>", Short: "Show active deterministic learning signals", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return withSessionContext(cmd, func(s *sessioncontext.Service) error {
		signals, err := s.Signals(args[0], false)
		if err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), signals)
	})
}}
var sessionJournalCmd = &cobra.Command{Use: "journal <session-id>", Short: "Show the bounded canonical session chronology", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	return withSessionContext(cmd, func(s *sessioncontext.Service) error {
		journal, err := s.Journal(args[0], 200)
		if err != nil {
			return err
		}
		if len(journal) == 0 {
			fmt.Fprintln(cmd.ErrOrStderr(), "(no structured journal events)")
		}
		return writeJSON(cmd.OutOrStdout(), journal)
	})
}}

func init() { sessionCmd.AddCommand(sessionStateCmd, sessionSignalsCmd, sessionJournalCmd) }
