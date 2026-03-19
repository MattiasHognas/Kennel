package ui

import (
	"testing"
)

func TestStylesConstants(t *testing.T) {
	if FocusedColor == nil {
		t.Errorf("FocusedColor should be initialized")
	}
	_ = HeaderStyle
	_ = SelectedFocusedStyle
}
