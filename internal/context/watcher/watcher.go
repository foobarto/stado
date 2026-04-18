package watcher

import (
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

type EventHandler func(event string, path string)

type Watcher struct {
	watcher  *fsnotify.Watcher
	handlers []EventHandler
	workdir  string
}

func New(workdir string, handler EventHandler) (*Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	ww := &Watcher{
		watcher:  w,
		handlers: []EventHandler{handler},
		workdir:  workdir,
	}

	go ww.loop()

	if err := w.Add(workdir); err != nil {
		return nil, err
	}

	return ww, nil
}

func (w *Watcher) loop() {
	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			
			relPath, _ := filepath.Rel(w.workdir, event.Name)
			if strings.HasPrefix(relPath, ".") || strings.HasPrefix(relPath, "node_modules") || strings.HasPrefix(relPath, ".git") {
				continue
			}

			var eventType string
			switch {
			case event.Op&fsnotify.Write != 0:
				eventType = "write"
			case event.Op&fsnotify.Create != 0:
				eventType = "create"
			case event.Op&fsnotify.Remove != 0 || event.Op&fsnotify.Rename != 0:
				eventType = "delete"
			}

			for _, h := range w.handlers {
				h(eventType, relPath)
			}

		case _, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
		}
	}
}

func (w *Watcher) Close() error {
	return w.watcher.Close()
}
