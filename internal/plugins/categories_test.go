package plugins_test

import (
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func TestValidateCategories_Known(t *testing.T) {
	if err := plugins.ValidateCategories([]string{"filesystem", "shell", "network"}); err != nil {
		t.Errorf("known categories should pass: %v", err)
	}
}

func TestValidateCategories_Unknown(t *testing.T) {
	err := plugins.ValidateCategories([]string{"filesystem", "netork"}) // typo
	if err == nil {
		t.Error("unknown category should fail")
	}
}

func TestValidateCategories_Empty(t *testing.T) {
	if err := plugins.ValidateCategories([]string{}); err != nil {
		t.Errorf("empty categories should pass (discouraged but valid): %v", err)
	}
}

func TestValidateCategories_AllCanonical(t *testing.T) {
	if err := plugins.ValidateCategories(plugins.CanonicalCategories); err != nil {
		t.Errorf("all canonical categories should pass: %v", err)
	}
}

func TestCanonicalCategories_Count(t *testing.T) {
	if got := len(plugins.CanonicalCategories); got != 21 {
		t.Errorf("expected 21 canonical categories (EP-0037 §C), got %d", got)
	}
}
