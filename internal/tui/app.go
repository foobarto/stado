package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/foobarto/stado/internal/config"
	projctx "github.com/foobarto/stado/internal/context"
	"github.com/foobarto/stado/internal/providers/anthropic"
	"github.com/foobarto/stado/internal/providers/gemini"
	"github.com/foobarto/stado/internal/providers/ollama"
	oai "github.com/foobarto/stado/internal/providers/openai"
	"github.com/foobarto/stado/internal/mcp"
	"github.com/foobarto/stado/internal/mcpbridge"
	"github.com/foobarto/stado/internal/storage"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tools/bash"
	fstools "github.com/foobarto/stado/internal/tools/fs"
	"github.com/foobarto/stado/internal/tools/task"
	"github.com/foobarto/stado/internal/tools/todo"
	"github.com/foobarto/stado/internal/tools/webfetch"
	"github.com/foobarto/stado/internal/tui/input"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/overlays"
	"github.com/foobarto/stado/internal/tui/palette"
	"github.com/foobarto/stado/internal/tui/theme"
	msgvp "github.com/foobarto/stado/internal/tui/viewport"
	"github.com/foobarto/stado/pkg/provider"
	"github.com/foobarto/stado/pkg/tool"
	"github.com/google/uuid"
)

type textDeltaMsg struct{ text string }
type toolCallMsg struct{ toolCalls []provider.ToolCall }
type toolsDoneMsg struct{}
type doneMsg struct{}
type errorMsg struct{ err error }

type approvalState struct {
	toolName string
	command  string
	args     map[string]any
}

type model struct {
	cfg       *config.Config
	db        *storage.DB
	prov      provider.Provider
	registry  *tools.Registry
	sessionID string
	workdir   string

	keys     *keys.Registry
	input    *input.Editor
	messages *msgvp.Model
	todos    *msgvp.TodoModel
	slash    *palette.Model

	width  int
	height int

	streaming    bool
	streamMu     sync.Mutex
	streamCancel func()

	approval *approvalState
	showHelp bool

	rawMessages []provider.Message

	p *tea.Program
}

func Run(cfg *config.Config) error {
	db, err := storage.Open(cfg.StateDir())
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	defer db.Close()

	var sessionID string
	var workdir string
	var rawMessages []provider.Message
	var displayMsgs []msgvp.DisplayMsg

	latestID, err := db.GetLatestSessionID(context.Background())
	if err == nil && latestID != "" {
		session, err := db.GetSession(context.Background(), latestID)
		if err == nil {
			sessionID = session.ID
			workdir = session.Workdir
			// Don't auto-override provider/model from previous session unless requested, 
			// use config defaults for new queries. Or, if we want strict resume:
			cfg.Defaults.Provider = session.Provider
			cfg.Defaults.Model = session.Model
			
			msgs, _ := db.GetMessages(context.Background(), sessionID)
			for _, m := range msgs {
				// Basic string unquote if it's JSON encoded
				var content string
				if err := json.Unmarshal([]byte(m.ContentJSON), &content); err != nil {
					// Fallback for raw strings if they weren't encoded
					content = m.ContentJSON
				}
				rawMessages = append(rawMessages, provider.Message{
					Role:    m.Role,
					Content: content,
				})
				displayMsgs = append(displayMsgs, msgvp.DisplayMsg{
					Role:    m.Role,
					Content: content,
				})
			}
		}
	}

	if sessionID == "" {
		sessionID = uuid.New().String()
		workdir, _ = os.Getwd()
		if err := db.CreateSession(context.Background(), sessionID, cfg.Defaults.Provider, cfg.Defaults.Model, workdir); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
	}

	var prov provider.Provider
	switch cfg.Defaults.Provider {
	case "anthropic":
		prov, err = anthropic.New("")
	case "openai":
		prov, err = oai.New("", "")
	case "gemini":
		prov, err = gemini.New("")
	case "ollama":
		prov, err = ollama.New("")
	default:
		if pCfg, ok := cfg.Providers[cfg.Defaults.Provider]; ok {
			switch pCfg.Kind {
			case "openai-compatible":
				prov, err = oai.New(os.Getenv(pCfg.APIKeyEnv), pCfg.BaseURL)
			default:
				return fmt.Errorf("unsupported provider kind: %s", pCfg.Kind)
			}
		} else {
			return fmt.Errorf("unsupported provider: %s", cfg.Defaults.Provider)
		}
	}
	if err != nil {
		return fmt.Errorf("provider: %w", err)
	}

	registry := tools.NewRegistry()
	registry.Register(fstools.ReadTool{})
	registry.Register(fstools.WriteTool{})
	registry.Register(fstools.EditTool{})
	registry.Register(fstools.GlobTool{})
	registry.Register(fstools.GrepTool{})
	registry.Register(bash.BashTool{Timeout: 30 * time.Second})
	registry.Register(todo.TodoTool{})
	registry.Register(webfetch.WebFetchTool{})
	registry.Register(task.TaskTool{
		Runner: func(ctx context.Context, desc, prompt string) (string, error) {
			return fmt.Sprintf("Simulated task '%s' completion. Received prompt: %s", desc, prompt), nil
		},
	})

	mcpManager := mcp.NewManager()
	defer mcpManager.Close()
	
	ctxEngine, err := projctx.New(workdir)
	if err != nil {
		return fmt.Errorf("create context engine: %w", err)
	}
	defer ctxEngine.Close()

	registry.Register(projctx.ContextTool{Engine: ctxEngine})
	
	// Load MCP config from config file
	// TODO: Load MCP servers from cfg.MCP section in M7
	// for _, srv := range cfg.MCP.Servers {
	//     if err := mcpManager.Connect(context.Background(), mcp.ServerConfig{...}); err != nil {
	//         log.Printf("MCP connect error: %v", err)
	//     }
	// }
	
	for _, mc := range mcpManager.AllClients() {
		for _, t := range mc.Tools() {
			registry.Register(mcpbridge.MCPTool{
				ServerName: mc.Name,
				Tool:       t,
				Client:     mc,
			})
		}
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(120),
	)
	if err != nil {
		return fmt.Errorf("glamour: %w", err)
	}

	keyReg := keys.NewRegistry()
	_ = keys.LoadOverrides(keyReg, cfg) // M5 stub

	m := &model{
		cfg:         cfg,
		db:          db,
		prov:        prov,
		registry:    registry,
		sessionID:   sessionID,
		workdir:     workdir,
		keys:        keyReg,
		input:       input.New(keyReg),
		messages:    msgvp.New(keyReg, r),
		todos:       msgvp.NewTodo(keyReg),
		slash:       palette.New(),
		rawMessages: rawMessages,
	}

	for _, dMsg := range displayMsgs {
		m.messages.Append(dMsg)
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	m.p = p

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		p.Send(tea.Quit)
	}()

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("tui: %w", err)
	}

	return nil
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(tea.EnterAltScreen, textarea.Blink)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.slash.Width = msg.Width
		return m, m.resize()

	case tea.KeyMsg:
		// Help overlay dismiss
		if m.showHelp {
			if m.keys.Matches(msg, keys.SessionInterrupt) || m.keys.Matches(msg, keys.TipsToggle) {
				m.showHelp = false
				return m, m.resize()
			}
			return m, nil
		}

		// Approval mode
		if m.approval != nil {
			if m.keys.Matches(msg, keys.Approve) {
				return m, m.handleApproval(true)
			}
			if m.keys.Matches(msg, keys.Deny) {
				return m, m.handleApproval(false)
			}
			return m, nil
		}

		// Slash palette intercepts
		if m.slash.Visible {
			cmd, handled := m.slash.Update(msg)
			if handled {
				return m, cmd
			}
			if m.keys.Matches(msg, keys.InputSubmit) {
				selected := m.slash.Selected()
				if selected != nil {
					m.input.Reset()
					m.slash.Visible = false
					return m, m.handleSlash(selected.Name)
				}
			}
		}

		// Global shortcuts
		switch {
		case m.keys.Matches(msg, keys.AppExit):
			return m, tea.Quit

		case m.keys.Matches(msg, keys.SessionInterrupt):
			if m.streaming && m.streamCancel != nil {
				m.streamCancel()
				return m, nil
			}

		case m.keys.Matches(msg, keys.TipsToggle):
			m.showHelp = true
			return m, m.resize()

		case m.keys.Matches(msg, keys.InputClear):
			if m.input.Value() == "" {
				return m, tea.Quit
			}
			// Handled by input below

		case m.keys.Matches(msg, keys.InputSubmit):
			if m.input.Value() == "" || m.streaming {
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}

			if strings.HasPrefix(text, "/") {
				m.input.Reset()
				m.slash.Visible = false
				return m, m.handleSlash(text)
			}

			m.input.History.Push(text)
			m.input.Reset()
			m.rawMessages = append(m.rawMessages, provider.Message{Role: "user", Content: text})
			
			// Save to DB
			b, _ := json.Marshal(text)
			m.db.AppendMessage(context.Background(), m.sessionID, uuid.New().String(), "user", string(b), len(m.rawMessages))
			
			m.messages.Append(msgvp.DisplayMsg{Role: "user", Content: text})
			return m, m.startStream()
		}
	}

	// Update components
	cmd, handled := m.messages.Update(msg)
	if handled {
		return m, cmd
	}

	oldVal := m.input.Value()
	cmd, _ = m.input.Update(msg)
	cmds = append(cmds, cmd)
	
	newVal := m.input.Value()
	if oldVal != newVal {
		m.slash.UpdateFilter(newVal)
		cmds = append(cmds, m.resize())
	}

	switch msg := msg.(type) {
	case textDeltaMsg:
		m.streamMu.Lock()
		m.messages.AppendTextToLast(msg.text)
		if len(m.rawMessages) > 0 && m.rawMessages[len(m.rawMessages)-1].Role == "assistant" {
			m.rawMessages[len(m.rawMessages)-1].Content += msg.text
		} else {
			m.rawMessages = append(m.rawMessages, provider.Message{Role: "assistant", Content: msg.text})
		}
		m.streamMu.Unlock()

	case toolCallMsg:
		for _, tc := range msg.toolCalls {
			m.messages.Append(msgvp.DisplayMsg{
				Role:    "tool_call",
				Content: fmt.Sprintf("%s(%s)", tc.Name, tc.Args),
			})
		}
		return m, m.executeTools(msg.toolCalls)

	case toolsDoneMsg:
		return m, m.startStream()

	case doneMsg:
		m.streaming = false
		// Save assistant's last message to DB
		if len(m.rawMessages) > 0 {
			lastMsg := m.rawMessages[len(m.rawMessages)-1]
			if lastMsg.Role == "assistant" {
				b, _ := json.Marshal(lastMsg.Content)
				m.db.AppendMessage(context.Background(), m.sessionID, uuid.New().String(), "assistant", string(b), len(m.rawMessages))
			}
		}

	case errorMsg:
		m.streaming = false
		m.messages.Append(msgvp.DisplayMsg{Role: "system", Content: fmt.Sprintf("error: %v", msg.err)})
	}

	return m, tea.Batch(cmds...)
}

func (m *model) resize() tea.Cmd {
	vpH := m.height - 3 // Header + StatusBar + border
	
	// Input height
	inH := strings.Count(m.input.Value(), "\n") + 1
	if inH > m.height/3 {
		inH = m.height/3
	}
	m.input.Model.SetHeight(inH)
	vpH -= inH

	if m.approval != nil {
		vpH -= 2
	}
	if m.slash.Visible {
		vpH -= len(m.slash.Matches)
	}

	if vpH < 1 {
		vpH = 1
	}

	msgW := m.width - 2
	if m.todos.HasTodos() {
		// Split viewport horizontally
		todoW := m.width / 4
		if todoW < 20 {
			todoW = 20
		}
		if todoW > 40 {
			todoW = 40
		}
		msgW = m.width - todoW - 4 // -4 for padding/borders
		m.todos.SetSize(todoW, vpH)
	}

	m.messages.SetSize(msgW, vpH)
	m.messages.Viewport.GotoBottom()
	return nil
}

func (m *model) handleSlash(text string) tea.Cmd {
	parts := strings.Fields(text)
	switch parts[0] {
	case "/clear":
		m.messages.Clear()
		m.rawMessages = nil
	case "/help":
		m.showHelp = true
	case "/exit", "/quit":
		return tea.Quit
	case "/model":
		if len(parts) < 2 {
			m.messages.Append(msgvp.DisplayMsg{
				Role:    "system",
				Content: fmt.Sprintf("current model: %s (usage: /model <name>)", m.cfg.Defaults.Model),
			})
		} else {
			m.cfg.Defaults.Model = parts[1]
			m.messages.Append(msgvp.DisplayMsg{
				Role:    "system",
				Content: fmt.Sprintf("model set to: %s", parts[1]),
			})
		}
	case "/provider":
		m.messages.Append(msgvp.DisplayMsg{
			Role:    "system",
			Content: fmt.Sprintf("current provider: %s (usage: /provider <name>) - requires restart", m.prov.Name()),
		})
	default:
		m.messages.Append(msgvp.DisplayMsg{
			Role:    "system",
			Content: fmt.Sprintf("unknown command: %s (type /help)", parts[0]),
		})
	}
	return m.resize()
}

func (m *model) startStream() tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.streaming = true
	m.streamCancel = cancel

	go func() {
		defer cancel()

		var toolDefs []provider.ToolDef
		for _, t := range m.registry.All() {
			schema, _ := json.Marshal(t.Schema())
			toolDefs = append(toolDefs, provider.ToolDef{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  schema,
			})
		}

		req := provider.Request{
			Messages: m.rawMessages,
			Model:    m.cfg.Defaults.Model,
			Tools:    toolDefs,
		}

		ch, err := m.prov.Stream(ctx, req)
		if err != nil {
			m.p.Send(errorMsg{err: err})
			return
		}

		var pendingCalls []provider.ToolCall
		var currentCall *provider.ToolCall

		for ev := range ch {
			switch ev.Type {
			case provider.EventTextDelta:
				m.p.Send(textDeltaMsg{text: ev.TextDelta})
			case provider.EventToolCallStart:
				currentCall = ev.ToolCall
			case provider.EventToolCallArgsDelta:
				if currentCall != nil {
					currentCall.Args += ev.ToolCall.Args
				}
			case provider.EventToolCallEnd:
				if ev.ToolCall != nil {
					pendingCalls = append(pendingCalls, *ev.ToolCall)
				}
			case provider.EventError:
				m.p.Send(errorMsg{err: ev.Err})
				return
			case provider.EventDone:
				if len(pendingCalls) > 0 {
					m.p.Send(toolCallMsg{toolCalls: pendingCalls})
				} else {
					m.p.Send(doneMsg{})
				}
				return
			}
		}
	}()

	return nil
}

func (m *model) handleApproval(allow bool) tea.Cmd {
	if allow {
		m.approval = nil
		m.resize()
		return m.startStream()
	}
	m.approval = nil
	m.messages.Append(msgvp.DisplayMsg{Role: "system", Content: "Tool execution denied by user"})
	m.resize()
	return m.startStream()
}

func (m *model) executeTools(toolCalls []provider.ToolCall) tea.Cmd {
	go func() {
		for _, tc := range toolCalls {
			t, ok := m.registry.Get(tc.Name)
			if !ok {
				m.rawMessages = append(m.rawMessages, provider.Message{
					Role:    "tool",
					Content: fmt.Sprintf(`{"id":"%s","content":"unknown tool: %s","is_error":true}`, tc.ID, tc.Name),
				})
				continue
			}

			var args map[string]any
			json.Unmarshal([]byte(tc.Args), &args)

			m.approval = &approvalState{
				toolName: tc.Name,
				command:  tc.Args,
				args:     args,
			}
			m.p.Send(tea.Msg(nil))

			for m.approval != nil {
				time.Sleep(100 * time.Millisecond)
			}

			result, err := t.Run(context.Background(), json.RawMessage(tc.Args), m)
			if err != nil {
				m.rawMessages = append(m.rawMessages, provider.Message{
					Role:    "tool",
					Content: fmt.Sprintf(`{"id":"%s","content":"%s","is_error":true}`, tc.ID, err.Error()),
				})
			} else {
				content := result.Content
				if result.Error != "" {
					content = result.Error
				}
				m.rawMessages = append(m.rawMessages, provider.Message{
					Role:    "tool",
					Content: fmt.Sprintf(`{"id":"%s","content":"%s","is_error":%t}`, tc.ID, content, result.Error != ""),
				})
			}
		}

		m.p.Send(toolsDoneMsg{})
	}()

	return nil
}

func (m *model) Approve(ctx context.Context, req tool.ApprovalRequest) (tool.Decision, error) {
	m.approval = &approvalState{
		toolName: req.Tool,
		command:  req.Command,
		args:     req.Args,
	}

	for {
		time.Sleep(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			return tool.DecisionDeny, ctx.Err()
		default:
			if m.approval == nil {
				return tool.DecisionAllow, nil
			}
		}
	}
}

func (m *model) Workdir() string {
	return m.workdir
}

func (m *model) UpdateTodos(ctx context.Context, todos []todo.Todo) error {
	m.todos.SetTodos(todos)
	m.resize() // trigger a resize to allocate panel width
	return nil
}

func (m *model) View() string {
	if m.showHelp {
		return overlays.RenderHelp(m.keys, m.width)
	}

	var b strings.Builder

	b.WriteString(theme.Header.Render(" stado ") + "\n\n")

	// Split panel or single view
	if m.todos.HasTodos() {
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top,
			m.messages.View(),
			"  ", // Gap
			theme.BorderStyle.Width(m.todos.Viewport.Width).Render(m.todos.View()),
		) + "\n")
	} else {
		b.WriteString(m.messages.View() + "\n")
	}

	if m.approval != nil {
		b.WriteString(theme.MsgTool.Render(
			fmt.Sprintf("⚠ %s: %s [y/N] ", m.approval.toolName, m.approval.command),
		) + "\n")
	}

	if m.slash.Visible {
		b.WriteString(m.slash.View() + "\n")
	}

	status := " "
	if m.streaming {
		status = theme.Spinner.Render(" ● ") + " thinking"
	} else if m.approval != nil {
		status = lipgloss.NewStyle().Foreground(theme.Warning).Render(" ● ") + " awaiting approval"
	} else {
		status = theme.StatusDot.Render(" ● ") + " ready"
	}
	status += theme.StatusBar.Render("  " + m.prov.Name() + "/" + m.cfg.Defaults.Model)
	b.WriteString(status + "\n")

	b.WriteString(theme.BorderStyle.Width(m.width - 4).Render(m.input.View()))

	return theme.App.Width(m.width).Height(m.height).Render(b.String())
}
