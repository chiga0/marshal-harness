package processsupervisor

import "testing"

func TestJournalCanonicalObjectPrefixTruncatedTokens(t *testing.T) {
	// This is parser-only: every possible interruption of real canonical
	// bytes is checked without repeatedly fsyncing a test journal.
	data := []byte(`{"authority":{"name":"a\\b\"c","n":123},"list":[true,false,null],"nested":{"x":"y"}}`)
	for end := 0; end < len(data); end++ {
		if !validCanonicalObjectPrefix(data[:end]) {
			t.Fatalf("legal prefix rejected at byte %d: %q", end, data[:end])
		}
	}
	if validCanonicalObjectPrefix(data) {
		t.Fatal("complete object admitted as incomplete prefix")
	}
}

func TestJournalCanonicalObjectPrefixRejectsCorruption(t *testing.T) {
	for _, raw := range []string{
		`x`, `[]`, `{"a":@`, `{"a":01`, `{"a":"\q`,
		`{"a":1,"a":`, `{"a":1]`, `{"a":1}x`, `{"a":1} `,
		`{ "a":`, `{"a":1, "b":`, `{"a":"ok","b":!`,
	} {
		if validCanonicalObjectPrefix([]byte(raw)) {
			t.Fatalf("corrupt prefix admitted: %q", raw)
		}
	}
}
