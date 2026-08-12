package config

import (
	"testing"

	"github.com/foobarto/stado/internal/instructions"
)

func TestMigratableDefaultSystemPromptTemplateSHA256(t *testing.T) {
	for _, sum := range []string{
		legacyDefaultSystemPromptTemplateSHA256,
		previousDefaultSystemPromptTemplateSHA256,
	} {
		if !isMigratableDefaultSystemPromptTemplateSHA256(sum) {
			t.Fatalf("default system prompt digest %q is not migratable", sum)
		}
	}
	if isMigratableDefaultSystemPromptTemplateSHA256("custom") {
		t.Fatal("custom system prompt digest should not be migratable")
	}
	if isLegacyDefaultSystemPromptTemplate([]byte(instructions.DefaultSystemPromptTemplate)) {
		t.Fatal("current default system prompt should not immediately migrate")
	}
}
