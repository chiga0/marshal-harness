package canonical

import "testing"

func TestDigestJSONIgnoresPropertyOrderAndWhitespace(t *testing.T) {
	t.Parallel()
	first, err := DigestJSON([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	second, err := DigestJSON([]byte(" { \"a\" : 1.0, \"b\" : 2 } \n"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("digests differ: %s != %s", first, second)
	}
	if _, err := DigestJSON([]byte(`{"a":}`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
