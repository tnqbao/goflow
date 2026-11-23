package web

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

func TestComponentsEscapeContent(t *testing.T) {
	for _, tt := range []struct {
		name      string
		component templ.Component
	}{
		{"badge", Badge("<script>")},
		{"empty state", EmptyState("<script>", "<script>")},
		{"layout title", Layout("<script>")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := tt.component.Render(context.Background(), &output); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), "<script>") || !strings.Contains(output.String(), "&lt;script&gt;") {
				t.Errorf("content not escaped: %s", output.String())
			}
		})
	}
}
