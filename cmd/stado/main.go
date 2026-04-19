package main

import (
	"fmt"
	"os"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tui"
	"github.com/spf13/cobra"
)

const version = "0.0.0-dev"

var rootCmd = &cobra.Command{
	Use:   "stado",
	Short: "AI CLI harness and editor",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		return tui.Run(cfg)
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print stado version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(version)
	},
}

var configPathCmd = &cobra.Command{
	Use:   "config-path",
	Short: "Print the path to the config file",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		fmt.Println(cfg.ConfigPath)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd, configPathCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
