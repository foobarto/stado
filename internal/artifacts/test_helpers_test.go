package artifacts

func testMemory(scope Scope, binding ScopeBinding, summary, content string) Artifact {
	return LearningArtifact(KindMemory, scope, binding, LearningData{Summary: summary, Content: content})
}

func testLesson(scope Scope, binding ScopeBinding, summary, trigger string) Artifact {
	return LearningArtifact(KindLesson, scope, binding, LearningData{Summary: summary, Trigger: trigger})
}
