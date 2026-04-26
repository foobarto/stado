package tasks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/google/uuid"
)

type Status string

const (
	StatusOpen       Status = "open"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
)

const (
	MaxIDBytes    = 128
	MaxTitleBytes = 256
	MaxBodyBytes  = 16 * 1024
	MaxTasks      = 1000
	MaxStoreBytes = 128 * 1024 * 1024

	lockWaitTimeout = 5 * time.Second
	lockStaleAfter  = 2 * time.Minute
	lockRetryDelay  = 25 * time.Millisecond
)

var processLock sync.Mutex

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
	id, err := normalizeID(id)
	if err != nil {
		return Task{}, err
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
	title, body, err := normalizeText(title, body)
	if err != nil {
		return Task{}, err
	}
	if status == "" {
		status = StatusOpen
	}
	if err := validateStatus(status); err != nil {
		return Task{}, err
	}
	var task Task
	err = s.withLock(func() error {
		tasks, err := s.load()
		if err != nil {
			return err
		}
		if len(tasks) >= MaxTasks {
			return fmt.Errorf("task limit reached (%d)", MaxTasks)
		}
		now := s.now()
		task = Task{
			ID:        s.newID(),
			Title:     title,
			Body:      body,
			Status:    status,
			CreatedAt: now,
			UpdatedAt: now,
		}
		tasks = append(tasks, task)
		return s.save(tasks)
	})
	return task, err
}

func (s Store) Update(id string, patch Patch) (Task, error) {
	id, err := normalizeID(id)
	if err != nil {
		return Task{}, err
	}
	var task Task
	err = s.withLock(func() error {
		tasks, err := s.load()
		if err != nil {
			return err
		}
		for i := range tasks {
			if tasks[i].ID != id {
				continue
			}
			if patch.Title != nil {
				title := strings.TrimSpace(*patch.Title)
				if err := validateTitle(title); err != nil {
					return err
				}
				tasks[i].Title = title
			}
			if patch.Body != nil {
				body := strings.TrimSpace(*patch.Body)
				if err := validateBody(body); err != nil {
					return err
				}
				tasks[i].Body = body
			}
			if patch.Status != nil {
				if err := validateStatus(*patch.Status); err != nil {
					return err
				}
				tasks[i].Status = *patch.Status
			}
			tasks[i].UpdatedAt = s.now()
			if err := s.save(tasks); err != nil {
				return err
			}
			task = tasks[i]
			return nil
		}
		return fmt.Errorf("task %q not found", id)
	})
	return task, err
}

func (s Store) Delete(id string) error {
	id, err := normalizeID(id)
	if err != nil {
		return err
	}
	return s.withLock(func() error {
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
	})
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
	root, name, err := s.storeRoot(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	f, err := root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("task store is not a regular file: %s", s.Path)
	}
	if info.Size() > MaxStoreBytes {
		return nil, fmt.Errorf("task store exceeds %d bytes", MaxStoreBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxStoreBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxStoreBytes {
		return nil, fmt.Errorf("task store exceeds %d bytes", MaxStoreBytes)
	}
	if len(bytes.TrimSpace(data)) == 0 {
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
	if err := validateLoadedTasks(tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func (s Store) save(tasks []Task) error {
	root, name, err := s.storeRoot(true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()

	data, err := json.MarshalIndent(tasks, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > MaxStoreBytes {
		return fmt.Errorf("task store exceeds %d bytes", MaxStoreBytes)
	}
	tmpName := "." + name + "." + uuid.NewString() + ".tmp"
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = root.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return root.Rename(tmpName, name)
}

func (s Store) withLock(fn func() error) error {
	processLock.Lock()
	defer processLock.Unlock()

	release, err := s.acquireLock()
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

func (s Store) acquireLock() (func(), error) {
	root, name, err := s.storeRoot(true)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(s.Path)
	lockName := name + ".lock"
	lockPath := filepath.Join(dir, lockName)
	deadline := time.Now().Add(lockWaitTimeout)
	for {
		f, err := root.OpenFile(lockName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, _ = fmt.Fprintf(f, "pid=%d\n", os.Getpid())
			if closeErr := f.Close(); closeErr != nil {
				_ = root.Remove(lockName)
				_ = root.Close()
				return nil, closeErr
			}
			return func() {
				_ = root.Remove(lockName)
				_ = root.Close()
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			_ = root.Close()
			return nil, err
		}
		if info, statErr := root.Stat(lockName); statErr == nil && time.Since(info.ModTime()) > lockStaleAfter {
			_ = root.Remove(lockName)
			continue
		}
		if time.Now().After(deadline) {
			_ = root.Close()
			return nil, fmt.Errorf("task store is locked: %s", lockPath)
		}
		time.Sleep(lockRetryDelay)
	}
}

func (s Store) storeRoot(createDir bool) (*os.Root, string, error) {
	if strings.TrimSpace(s.Path) == "" {
		return nil, "", errors.New("task store path is empty")
	}
	dir := filepath.Dir(s.Path)
	name := filepath.Base(s.Path)
	if name == "." || name == ".." || name == string(filepath.Separator) || strings.Contains(name, "\x00") {
		return nil, "", fmt.Errorf("invalid task store path: %s", s.Path)
	}
	if createDir {
		if err := workdirpath.MkdirAllNoSymlink(dir, 0o700); err != nil {
			return nil, "", err
		}
	}
	root, err := workdirpath.OpenRootNoSymlink(dir)
	if err != nil {
		return nil, "", err
	}
	return root, name, nil
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

func normalizeID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("task id is required")
	}
	if len(id) > MaxIDBytes {
		return "", fmt.Errorf("task id exceeds %d bytes", MaxIDBytes)
	}
	return id, nil
}

func normalizeText(title, body string) (string, string, error) {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if err := validateTitle(title); err != nil {
		return "", "", err
	}
	if err := validateBody(body); err != nil {
		return "", "", err
	}
	return title, body, nil
}

func validateTitle(title string) error {
	if title == "" {
		return errors.New("task title is required")
	}
	if len(title) > MaxTitleBytes {
		return fmt.Errorf("task title exceeds %d bytes", MaxTitleBytes)
	}
	return nil
}

func validateBody(body string) error {
	if len(body) > MaxBodyBytes {
		return fmt.Errorf("task body exceeds %d bytes", MaxBodyBytes)
	}
	return nil
}

func validateLoadedTasks(tasks []Task) error {
	if len(tasks) > MaxTasks {
		return fmt.Errorf("task count exceeds %d", MaxTasks)
	}
	for i := range tasks {
		id, err := normalizeID(tasks[i].ID)
		if err != nil {
			return fmt.Errorf("task %d: %w", i, err)
		}
		tasks[i].ID = id
		tasks[i].Title = strings.TrimSpace(tasks[i].Title)
		tasks[i].Body = strings.TrimSpace(tasks[i].Body)
		if err := validateTitle(tasks[i].Title); err != nil {
			return fmt.Errorf("task %d: %w", i, err)
		}
		if err := validateBody(tasks[i].Body); err != nil {
			return fmt.Errorf("task %d: %w", i, err)
		}
		if err := validateStatus(tasks[i].Status); err != nil {
			return fmt.Errorf("task %d: %w", i, err)
		}
	}
	return nil
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
