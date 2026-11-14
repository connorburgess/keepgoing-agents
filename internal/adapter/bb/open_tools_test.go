package bb

import (
	"encoding/json"
	"strings"
	"testing"
)

// The cursor keeps the turn's open tool items under a hash of their IDs:
// no item ID is kept between checks, and a completion still closes the item
// it names.
func TestOpenToolsKeepNoItemID(t *testing.T) {
	var c Cursor
	item := json.RawMessage(`{"item":{"id":"item_private1","type":"commandExecution"}}`)
	c.trackItem(event{Seq: 1, Type: "item/started", Data: item})
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.OpenTools) != 1 || strings.Contains(string(b), "item_private1") {
		t.Fatalf("%s", b)
	}
	c.trackItem(event{Seq: 2, Type: "item/completed", Data: item})
	if len(c.OpenTools) != 0 || c.ToolsUncertain {
		t.Fatalf("%+v", c)
	}
}
