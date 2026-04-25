package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Status string

const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
)

type Task struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Patch struct {
	Title  *string
	Body   *string
	Status *Status
}

type Store struct {
	Path  string
	Now   func() time.Time
	NewID func() string
}

func StorePath(stateDir string) string {
	return filepath.Join(stateDir, "tasks", "tasks.json")
}

func (s Store) List(status Status) ([]Task, error) {
	tasks, err := s.load()
	if err != nil {
		return nil, err
	}
	if status != "" {
		if err := validateStatus(status); err != nil {
			return nil, err
		}
		filtered := tasks[:0]
		for _, task := range tasks {
			if task.Status == status {
				filtered = append(filtered, task)
			}
		}
		tasks = filtered
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Status != tasks[j].Status {
			return statusRank(tasks[i].Status) < statusRank(tasks[j].Status)
		}
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	return tasks, nil
}

func (s Store) Get(id string) (Task, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Task{}, errors.New("task id is required")
	}
	tasks, err := s.load()
	if err != nil {
		return Task{}, err
	}
	for _, task := range tasks {
		if task.ID == id {
			return task, nil
		}
	}
	return Task{}, fmt.Errorf("task %q not found", id)
}

func (s Store) Create(title, body string, status Status) (Task, error) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if title == "" {
		return Task{}, errors.New("task title is required")
	}
	if status == "" {
		status = StatusOpen
	}
	if err := validateStatus(status); err != nil {
		return Task{}, err
	}
	tasks, err := s.load()
	if err != nil {
		return Task{}, err
	}
	now := s.now()
	task := Task{
		ID:        s.newID(),
		Title:     title,
		Body:      body,
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
	}
	tasks = append(tasks, task)
	if err := s.save(tasks); err != nil {
		return Task{}, err
	}
	return task, nil
}

func (s Store) Update(id string, patch Patch) (Task, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Task{}, errors.New("task id is required")
	}
	tasks, err := s.load()
	if err != nil {
		return Task{}, err
	}
	for i := range tasks {
		if tasks[i].ID != id {
			continue
		}
		if patch.Title != nil {
			title := strings.TrimSpace(*patch.Title)
			if title == "" {
				return Task{}, errors.New("task title is required")
			}
			tasks[i].Title = title
		}
		if patch.Body != nil {
			tasks[i].Body = strings.TrimSpace(*patch.Body)
		}
		if patch.Status != nil {
			if err := validateStatus(*patch.Status); err != nil {
				return Task{}, err
			}
			tasks[i].Status = *patch.Status
		}
		tasks[i].UpdatedAt = s.now()
		if err := s.save(tasks); err != nil {
			return Task{}, err
		}
		return tasks[i], nil
	}
	return Task{}, fmt.Errorf("task %q not found", id)
}

func (s Store) Delete(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("task id is required")
	}
	tasks, err := s.load()
	if err != nil {
		return err
	}
	for i := range tasks {
		if tasks[i].ID == id {
			tasks = append(tasks[:i], tasks[i+1:]...)
			return s.save(tasks)
		}
	}
	return fmt.Errorf("task %q not found", id)
}

func ParseStatus(value string) (Status, error) {
	status := Status(strings.TrimSpace(strings.ToLower(value)))
	if status == "" {
		return StatusOpen, nil
	}
	if err := validateStatus(status); err != nil {
		return "", err
	}
	return status, nil
}

func (s Store) load() ([]Task, error) {
	if strings.TrimSpace(s.Path) == "" {
		return nil, errors.New("task store path is empty")
	}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, nil
	}
	var tasks []Task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, fmt.Errorf("read tasks: %w", err)
	}
	for i := range tasks {
		if tasks[i].Status == "" {
			tasks[i].Status = StatusOpen
		}
	}
	return tasks, nil
}

func (s Store) save(tasks []Task) error {
	if strings.TrimSpace(s.Path) == "" {
		return errors.New("task store path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".tasks-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return err
	}
	return os.Rename(tmpName, s.Path)
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s Store) newID() string {
	if s.NewID != nil {
		if id := strings.TrimSpace(s.NewID()); id != "" {
			return id
		}
	}
	return uuid.NewString()
}

func validateStatus(status Status) error {
	switch status {
	case StatusOpen, StatusInProgress, StatusDone:
		return nil
	default:
		return fmt.Errorf("invalid task status %q", status)
	}
}

func statusRank(status Status) int {
	switch status {
	case StatusOpen:
		return 0
	case StatusInProgress:
		return 1
	case StatusDone:
		return 2
	default:
		return 3
	}
}
