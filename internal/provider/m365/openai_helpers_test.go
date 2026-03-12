package m365

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestExtractTurnData_ImageURLObject(t *testing.T) {
	item := gjson.Parse(`{
  "type":"message",
  "role":"user",
  "content":[
    {"type":"input_text","text":"hello"},
    {"type":"input_image","image_url":{"url":"data:image/png;base64,AAAA"}}
  ]
}`)
	turn := extractTurnData([]gjson.Result{item})
	if turn.UserTaskText != "hello" {
		t.Fatalf("unexpected user text: %q", turn.UserTaskText)
	}
	if len(turn.ImageURLs) != 1 {
		t.Fatalf("expected 1 image url, got %d", len(turn.ImageURLs))
	}
	if turn.ImageURLs[0] != "data:image/png;base64,AAAA" {
		t.Fatalf("unexpected image url: %q", turn.ImageURLs[0])
	}
}
