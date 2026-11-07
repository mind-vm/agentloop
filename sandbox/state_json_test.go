package sandbox

import (
	"encoding/json"
	"strings"
	"testing"
)

// state is saved the way JSON.stringify saves it, because that is what a
// script's author expects from "saved as JSON". Go's encoder refused NaN,
// Infinity and functions outright, and the refusal came back as no state at
// all: one NaN from Number("n/a") emptied state, the next script started
// from {}, and nothing said so.
func TestStateIsSavedLikeJSONStringify(t *testing.T) {
	sb := New()
	_, ret, err := sb.ExecuteTurn(`
		state.rows = [1, 2];
		state.nan = NaN;
		state.inf = -Infinity;
		state.fn = function () { return 1; };
		state.undef = undefined;
		state.list = [1, undefined, NaN, function () {}];
		state.date = new Date(Date.UTC(2026, 0, 2));
		state.map = new Map([["a", 1]]);
		state.custom = { toJSON: function () { return "custom"; } };`, nil)
	if err != nil {
		t.Fatalf("saving state failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(ret, &got); err != nil {
		t.Fatalf("state is not JSON: %v (%s)", err, ret)
	}
	want := `{"custom":"custom","date":"2026-01-02T00:00:00.000Z","inf":null,"list":[1,null,null,null],"map":{},"nan":null,"rows":[1,2]}`
	if b, _ := json.Marshal(got); string(b) != want {
		t.Fatalf("state saved as\n %s\nwant\n %s", b, want)
	}
}

// What JSON.stringify throws on, the script throws on: the model gets an
// error it can fix, and the state it had before stays.
func TestStateThatCannotBeSavedIsAnError(t *testing.T) {
	sb := New()
	logs, ret, err := sb.ExecuteTurn(`state.self = {}; state.self.loop = state.self; log("ran")`, json.RawMessage(`{"rows":[1]}`))
	if err == nil {
		t.Fatalf("a cyclic state was accepted: %s", ret)
	}
	if ret != nil {
		t.Errorf("ret = %s, want nil so the caller keeps the previous state", ret)
	}
	if !strings.Contains(err.Error(), "state") || !strings.Contains(err.Error(), "circular") {
		t.Errorf("the error does not say what failed: %v", err)
	}
	if logs != "ran" {
		t.Errorf("logs = %q; what the script logged before saving still belongs to the model", logs)
	}
}
