package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/brokercredential"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/daemon"
	"github.com/foobarto/stado/internal/sessioncontext"
	"github.com/spf13/cobra"
)

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func withSessionContext(cmd *cobra.Command, subject string, fn func(*daemon.Client, broker.SessionContextReadAuth) error) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	credentials, err := brokercredential.New(cfg.StateDir())
	if err != nil {
		return err
	}
	credential, err := credentials.Load(subject)
	if err != nil {
		return fmt.Errorf("session context credential: %w", err)
	}
	socketPath, err := daemon.SocketPath()
	if err != nil {
		return err
	}
	stadoBin, err := os.Executable()
	if err != nil {
		return err
	}
	dialCtx, dialCancel := context.WithTimeout(cmd.Context(), brokerAttachTimeout)
	defer dialCancel()
	client, _, err := daemon.EnsureRunning(dialCtx, socketPath, stadoBin, brokerAttachTimeout)
	if err != nil {
		return fmt.Errorf("session context broker: %w", err)
	}
	defer func() { _ = client.Close() }()
	return fn(client, broker.SessionContextReadAuth{
		Subject: credential.Subject, Ticket: credential.Ticket, ResumeSecret: credential.ResumeSecret,
	})
}

var sessionStateCmd = &cobra.Command{Use: "state <session-id>", Short: "Show the bounded structured session state", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	var state sessioncontext.State
	err := withSessionContext(cmd, args[0], func(client *daemon.Client, auth broker.SessionContextReadAuth) error {
		callCtx, cancel := context.WithTimeout(cmd.Context(), brokerAttachTimeout)
		defer cancel()
		if err := client.Call(callCtx, broker.MethodSessionContextState, broker.SessionContextStateParams(auth), &state); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return writeJSON(cmd.OutOrStdout(), state)
}}
var sessionSignalsCmd = &cobra.Command{Use: "signals <session-id>", Short: "Show active deterministic learning signals", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	var signals []sessioncontext.Signal
	err := withSessionContext(cmd, args[0], func(client *daemon.Client, auth broker.SessionContextReadAuth) error {
		callCtx, cancel := context.WithTimeout(cmd.Context(), brokerAttachTimeout)
		defer cancel()
		if err := client.Call(callCtx, broker.MethodSessionContextSignals, broker.SessionContextSignalsParams{SessionContextReadAuth: auth}, &signals); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	return writeJSON(cmd.OutOrStdout(), signals)
}}
var sessionJournalCmd = &cobra.Command{Use: "journal <session-id>", Short: "Show the bounded canonical session chronology", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	var journal []sessioncontext.JournalEntry
	err := withSessionContext(cmd, args[0], func(client *daemon.Client, auth broker.SessionContextReadAuth) error {
		callCtx, cancel := context.WithTimeout(cmd.Context(), brokerAttachTimeout)
		defer cancel()
		if err := client.Call(callCtx, broker.MethodSessionContextJournal, broker.SessionContextJournalParams{SessionContextReadAuth: auth, Limit: 200}, &journal); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(journal) == 0 {
		fmt.Fprintln(cmd.ErrOrStderr(), "(no structured journal events)")
	}
	return writeJSON(cmd.OutOrStdout(), journal)
}}

func init() { sessionCmd.AddCommand(sessionStateCmd, sessionSignalsCmd, sessionJournalCmd) }
