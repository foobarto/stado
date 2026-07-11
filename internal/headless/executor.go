package headless

import (
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
)

func (s *Server) buildExecutor(sess *stadogit.Session) (*tools.Executor, error) {
	exec, err := runtime.BuildExecutor(sess, s.Cfg, "stado-headless", s.Metrics)
	if err != nil {
		return nil, err
	}
	s.ExecutorSandbox.Apply(exec)
	return exec, nil
}
