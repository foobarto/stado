package acp

import (
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
)

func (s *Server) buildExecutor(sess *stadogit.Session) (*tools.Executor, error) {
	return s.buildExecutorWithSandbox(sess, s.ExecutorSandbox)
}

func (s *Server) buildExecutorWithSandbox(sess *stadogit.Session, executorSandbox runtime.ExecutorSandbox) (*tools.Executor, error) {
	exec, err := runtime.BuildExecutor(sess, s.Cfg, "stado-acp", s.Metrics)
	if err != nil {
		return nil, err
	}
	executorSandbox.Apply(exec)
	return exec, nil
}

func (s *Server) executorSandboxFor(sess *acpSession) runtime.ExecutorSandbox {
	if sess != nil && sess.broker != nil {
		return sess.broker.Sandbox()
	}
	return s.ExecutorSandbox
}
