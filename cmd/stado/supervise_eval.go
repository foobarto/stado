package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	superviseeval "github.com/foobarto/stado/internal/supervise/eval"
)

var superviseEvalCmd = &cobra.Command{
	Use:   "supervise-eval",
	Short: "Validate and score paired supervised-work evaluations",
}

var superviseEvalScenarioCmd = &cobra.Command{
	Use:   "scenario <scenario.json>",
	Short: "Validate and print one supervision evaluation scenario",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		scenario, err := superviseeval.LoadScenario(args[0])
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(scenario)
	},
}

var superviseEvalScoreInput string

var superviseEvalScoreCmd = &cobra.Command{
	Use:   "score",
	Short: "Score paired unsupervised/supervised JSONL observations",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		in := cmd.InOrStdin()
		if superviseEvalScoreInput != "" && superviseEvalScoreInput != "-" {
			file, err := os.Open(superviseEvalScoreInput)
			if err != nil {
				return err
			}
			defer file.Close()
			in = file
		}
		observations, err := superviseeval.DecodeObservations(in)
		if err != nil {
			return err
		}
		comparisons, err := superviseeval.Compare(observations)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(comparisons); err != nil {
			return fmt.Errorf("encode comparisons: %w", err)
		}
		return nil
	},
}

func init() {
	superviseEvalScoreCmd.Flags().StringVarP(&superviseEvalScoreInput, "input", "i", "-", "JSONL observations file ('-' for stdin)")
	superviseEvalCmd.AddCommand(superviseEvalScenarioCmd, superviseEvalScoreCmd)
	rootCmd.AddCommand(superviseEvalCmd)
}
