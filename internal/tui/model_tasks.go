package tui

import (
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/tasks"
	"github.com/foobarto/stado/internal/tui/taskpicker"
)

func (m *Model) taskStore() (tasks.Store, error) {
	cfg, err := m.sessionActionConfig()
	if err != nil {
		return tasks.Store{}, err
	}
	return tasks.Store{Path: tasks.StorePath(cfg.StateDir())}, nil
}

func (m *Model) openTaskPicker() error {
	store, err := m.taskStore()
	if err != nil {
		return err
	}
	items, err := store.List("")
	if err != nil {
		return err
	}
	if m.taskPick == nil {
		m.taskPick = taskpicker.New()
	}
	m.taskPick.Open(items, "")
	return nil
}

func (m *Model) applyTaskCommand(cmd taskpicker.Command) error {
	if cmd.Type == taskpicker.CommandNone {
		return nil
	}
	store, err := m.taskStore()
	if err != nil {
		return err
	}
	switch cmd.Type {
	case taskpicker.CommandCreate:
		task, err := store.Create(cmd.Title, cmd.Body, cmd.Status)
		if err != nil {
			return err
		}
		return m.reloadTaskPicker(task.ID, true)
	case taskpicker.CommandUpdate:
		title := cmd.Title
		body := cmd.Body
		status := cmd.Status
		task, err := store.Update(cmd.ID, tasks.Patch{Title: &title, Body: &body, Status: &status})
		if err != nil {
			return err
		}
		return m.reloadTaskPicker(task.ID, true)
	case taskpicker.CommandDelete:
		if err := store.Delete(cmd.ID); err != nil {
			return err
		}
		return m.reloadTaskPicker("", false)
	default:
		return nil
	}
}

func (m *Model) reloadTaskPicker(selectedID string, detail bool) error {
	store, err := m.taskStore()
	if err != nil {
		return err
	}
	items, err := store.List("")
	if err != nil {
		return err
	}
	if m.taskPick == nil {
		m.taskPick = taskpicker.New()
	}
	m.taskPick.Open(items, selectedID)
	if detail && selectedID != "" {
		m.taskPick.ShowDetail(selectedID)
	}
	return nil
}

func (m *Model) createTaskFromSlash(title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("usage: /tasks add <title>")
	}
	store, err := m.taskStore()
	if err != nil {
		return err
	}
	task, err := store.Create(title, "", tasks.StatusOpen)
	if err != nil {
		return err
	}
	m.appendBlock(block{kind: "system", body: "task created: " + task.Title})
	return nil
}
