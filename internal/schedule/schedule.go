// Package schedule manages persistent scheduled stado runs. Entries
// are stored in <StateDir>/schedules.json and executed via OS cron
// (stado schedule install-cron) or on-demand (stado schedule run-now).
// EP-0036.
package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Entry is a single scheduled run.
type Entry struct {
	ID        string    `json:"id"`
	Name      string    `json:"name,omitempty"`
	Cron      string    `json:"cron"`
	Prompt    string    `json:"prompt"`
	SessionID string    `json:"session_id,omitempty"`
	Created   time.Time `json:"created"`
}

// Store reads and writes the schedules.json file.
type Store struct {
	Path string // absolute path to schedules.json
}

func (s *Store) load() ([]Entry, error) {
	data, err := os.ReadFile(s.Path) //nolint:gosec
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse schedules: %w", err)
	}
	return entries, nil
}

func (s *Store) save(entries []Entry) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.Path, data, 0o600) //nolint:gosec
}

// Create adds a new schedule entry and returns it.
func (s *Store) Create(cron, prompt, name, sessionID string) (Entry, error) {
	if err := validateCron(cron); err != nil {
		return Entry{}, fmt.Errorf("invalid cron expression: %w", err)
	}
	if strings.TrimSpace(prompt) == "" {
		return Entry{}, errors.New("prompt is required")
	}
	entries, err := s.load()
	if err != nil {
		return Entry{}, err
	}
	e := Entry{
		ID:        uuid.New().String(),
		Name:      name,
		Cron:      cron,
		Prompt:    prompt,
		SessionID: sessionID,
		Created:   time.Now().UTC(),
	}
	entries = append(entries, e)
	return e, s.save(entries)
}

// List returns all schedule entries.
func (s *Store) List() ([]Entry, error) { return s.load() }

// Remove deletes the entry with the given ID.
func (s *Store) Remove(id string) error {
	entries, err := s.load()
	if err != nil {
		return err
	}
	var out []Entry
	found := false
	for _, e := range entries {
		if e.ID == id {
			found = true
			continue
		}
		out = append(out, e)
	}
	if !found {
		return fmt.Errorf("schedule %q not found", id)
	}
	return s.save(out)
}

// Get returns the entry with the given ID.
func (s *Store) Get(id string) (Entry, error) {
	entries, err := s.load()
	if err != nil {
		return Entry{}, err
	}
	for _, e := range entries {
		if e.ID == id {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("schedule %q not found", id)
}

// Run executes the schedule entry immediately by calling `stado run`.
// stadoBin is the path to the stado binary. Output is appended to
// logPath (if non-empty). This is the function called by OS cron.
func (s *Store) Run(id, stadoBin, logPath string) error {
	e, err := s.Get(id)
	if err != nil {
		return err
	}
	args := []string{"run", "--prompt", e.Prompt, "--no-turn-limit"}
	if e.SessionID != "" {
		args = append(args, "--session-id", e.SessionID)
	}
	cmd := exec.Command(stadoBin, args...) //nolint:gosec
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec
		if err != nil {
			return err
		}
		defer f.Close()
		ts := fmt.Sprintf("\n--- %s ---\n", time.Now().UTC().Format(time.RFC3339))
		_, _ = f.WriteString(ts)
		cmd.Stdout = f
		cmd.Stderr = f
	}
	return cmd.Run()
}

// crontab sentinel prefix so we can identify stado-managed entries.
const crontabSentinel = "# stado:"

// InstallCron adds or replaces crontab entries for all active schedules.
// stadoBin is the absolute path to the stado binary.
func (s *Store) InstallCron(stadoBin string) error {
	entries, err := s.load()
	if err != nil {
		return err
	}
	existing, err := readCrontab()
	if err != nil {
		return err
	}
	// Remove all existing stado-managed lines.
	var filtered []string
	for _, line := range existing {
		if !strings.Contains(line, crontabSentinel) {
			filtered = append(filtered, line)
		}
	}
	// Append new entries.
	logBase := filepath.Dir(s.Path)
	for _, e := range entries {
		logPath := filepath.Join(logBase, "schedule-"+e.ID+".log")
		line := fmt.Sprintf("%s %s schedule run-now %s >> %s 2>&1 %s%s",
			e.Cron, stadoBin, e.ID, logPath, crontabSentinel, e.ID)
		filtered = append(filtered, line)
	}
	return writeCrontab(filtered)
}

// UninstallCron removes all stado-managed crontab entries.
func UninstallCron() error {
	existing, err := readCrontab()
	if err != nil {
		return err
	}
	var filtered []string
	for _, line := range existing {
		if !strings.Contains(line, crontabSentinel) {
			filtered = append(filtered, line)
		}
	}
	return writeCrontab(filtered)
}

func readCrontab() ([]string, error) {
	out, err := exec.Command("crontab", "-l").Output() //nolint:gosec
	if err != nil {
		// crontab -l exits 1 when there are no entries on some systems.
		if strings.Contains(err.Error(), "no crontab") ||
			strings.Contains(string(out), "no crontab") {
			return nil, nil
		}
		return nil, fmt.Errorf("crontab -l: %w", err)
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

func writeCrontab(lines []string) error {
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	cmd := exec.Command("crontab", "-") //nolint:gosec
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("crontab -: %w\n%s", err, out)
	}
	return nil
}

// validateCron checks that the expression has exactly 5 fields.
func validateCron(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("expected 5 fields (min hour dom month dow), got %d", len(fields))
	}
	return nil
}

// StorePath returns the default path for the schedules JSON file.
func StorePath(stateDir string) string {
	return filepath.Join(stateDir, "schedules.json")
}
