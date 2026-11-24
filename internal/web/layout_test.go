package web

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestLayoutEscapesTitle(t *testing.T) {
	var output bytes.Buffer
	if err := Layout("<script>").Render(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "<script>") || !strings.Contains(output.String(), "&lt;script&gt;") {
		t.Errorf("content not escaped: %s", output.String())
	}
}
