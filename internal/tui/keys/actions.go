package keys

type Action string

const (
	AppExit                 Action = "app_exit"
	SessionInterrupt        Action = "session_interrupt"
	HistoryPrevious         Action = "history_previous"
	HistoryNext             Action = "history_next"
	InputSubmit             Action = "input_submit"
	InputNewline            Action = "input_newline"
	InputClear              Action = "input_clear"
	InputMoveLeft           Action = "input_move_left"
	InputMoveRight          Action = "input_move_right"
	InputWordForward        Action = "input_word_forward"
	InputWordBackward       Action = "input_word_backward"
	InputLineHome           Action = "input_line_home"
	InputLineEnd            Action = "input_line_end"
	InputDeleteToLineEnd    Action = "input_delete_to_line_end"
	InputDeleteToLineStart  Action = "input_delete_to_line_start"
	InputDeleteWordForward  Action = "input_delete_word_forward"
	InputDeleteWordBackward Action = "input_delete_word_backward"
	InputBackspace          Action = "input_backspace"
	InputDelete             Action = "input_delete"
	MessagesPageUp          Action = "messages_page_up"
	MessagesPageDown        Action = "messages_page_down"
	MessagesHalfPageUp      Action = "messages_half_page_up"
	MessagesHalfPageDown    Action = "messages_half_page_down"
	MessagesFirst           Action = "messages_first"
	MessagesLast            Action = "messages_last"
	CommandList             Action = "command_list"
	AgentSwitch             Action = "agent_switch"
	ModelSwitch             Action = "model_switch"
	SessionSwitch           Action = "session_switch"
	SessionNew              Action = "session_new"
	ThemeSwitch             Action = "theme_switch"
	StatusView              Action = "status_view"
	ThinkingToggle          Action = "thinking_toggle"
	TipsToggle              Action = "tips_toggle"
	Approve                 Action = "approve"
	Deny                    Action = "deny"
	EditSummary             Action = "edit_summary"
	SidebarToggle           Action = "sidebar_toggle"
	SidebarNarrower         Action = "sidebar_narrower"
	SidebarWider            Action = "sidebar_wider"
	ToolExpand              Action = "tool_expand"
	ModeToggle              Action = "mode_toggle"
	ModeToggleBtw           Action = "mode_toggle_btw"
)

var ActionDescriptions = map[Action]string{
	AppExit:                 "Exit application",
	SessionInterrupt:        "Interrupt model",
	HistoryPrevious:         "Previous message",
	HistoryNext:             "Next message",
	InputSubmit:             "Submit",
	InputNewline:            "New line",
	InputClear:              "Clear input",
	InputMoveLeft:           "Move left",
	InputMoveRight:          "Move right",
	InputWordForward:        "Move word forward",
	InputWordBackward:       "Move word backward",
	InputLineHome:           "Move to line start",
	InputLineEnd:            "Move to line end",
	InputDeleteToLineEnd:    "Delete to line end",
	InputDeleteToLineStart:  "Delete to line start",
	InputDeleteWordForward:  "Delete word forward",
	InputDeleteWordBackward: "Delete word backward",
	InputBackspace:          "Backspace",
	InputDelete:             "Delete",
	MessagesPageUp:          "Scroll up",
	MessagesPageDown:        "Scroll down",
	MessagesHalfPageUp:      "Scroll half page up",
	MessagesHalfPageDown:    "Scroll half page down",
	MessagesFirst:           "Scroll to top",
	MessagesLast:            "Scroll to bottom",
	CommandList:             "Command palette",
	AgentSwitch:             "Switch agent",
	ModelSwitch:             "Switch model",
	SessionSwitch:           "Session manager",
	SessionNew:              "New session",
	ThemeSwitch:             "Switch theme",
	StatusView:              "Status modal",
	ThinkingToggle:          "Cycle thinking display",
	TipsToggle:              "Toggle help",
	Approve:                 "Allow",
	Deny:                    "Deny",
	EditSummary:             "Edit compaction summary",
	SidebarToggle:           "Toggle sidebar",
	SidebarNarrower:         "Make sidebar narrower",
	SidebarWider:            "Make sidebar wider",
	ToolExpand:              "Expand latest details",
	ModeToggle:              "Toggle Plan/Do mode",
	ModeToggleBtw:           "Toggle BTW mode",
}
