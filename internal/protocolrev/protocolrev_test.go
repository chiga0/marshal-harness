package protocolrev

import (
	"errors"
	"strings"
	"testing"

	"github.com/chiga0/marshal-harness/internal/canonical"
)

func digestOf(parts ...string) string {
	return canonical.DigestBytes([]byte(strings.Join(parts, "|")))
}

const testErrPrefix = "protocolrev: "

func requireTypedError(t *testing.T, err error, sentinel error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %v, got nil", sentinel)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected errors.Is(%v, %v) to hold", err, sentinel)
	}
	if !strings.HasPrefix(err.Error(), testErrPrefix) {
		t.Errorf("error %q missing %q prefix", err.Error(), testErrPrefix)
	}
}

func mustPin(t *testing.T, family, version string) PinnedRevision {
	t.Helper()
	p, err := NewPinnedRevision(family, version)
	if err != nil {
		t.Fatalf("NewPinnedRevision(%q, %q): %v", family, version, err)
	}
	return p
}

func mustMigration(t *testing.T, fromDigest, toDigest string, from, to Revision, prov string) SupersedeMigration {
	t.Helper()
	m, err := NewSupersedeMigration(fromDigest, toDigest, from, to, prov)
	if err != nil {
		t.Fatalf("NewSupersedeMigration: %v", err)
	}
	return m
}

// ── Revision parse matrix ───────────────────────────────────────────────────

func TestParseRevision_Valid(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		family  string
		version string
	}{
		{"unversioned acp", "acp", "acp", ""},
		{"versioned acp/v1", "acp/v1", "acp", "v1"},
		{"versioned mcp/v2", "mcp/v2", "mcp", "v2"},
		{"hyphen in family", "my-proto/v10", "my-proto", "v10"},
		{"single char family", "a/b", "a", "b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, err := ParseRevision(tc.raw)
			if err != nil {
				t.Fatalf("ParseRevision(%q): %v", tc.raw, err)
			}
			if r.Family != tc.family || r.Version != tc.version {
				t.Errorf("ParseRevision(%q) = %+v, want family=%q version=%q", tc.raw, r, tc.family, tc.version)
			}
			if got := r.String(); got != tc.raw {
				t.Errorf("String() = %q, want round-trip %q", got, tc.raw)
			}
			if want := tc.version != ""; r.Versioned() != want {
				t.Errorf("Versioned() = %v, want %v", r.Versioned(), want)
			}
		})
	}
}

func TestParseRevision_Malformed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"empty family", "/v1"},
		{"empty version", "acp/"},
		{"bare slash", "/"},
		{"more than one slash", "acp/v1/extra"},
		{"three slashes", "a/b/c/d"},
		{"leading whitespace", " acp"},
		{"trailing whitespace", "acp "},
		{"whitespace around slash", "acp /v1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRevision(tc.raw)
			requireTypedError(t, err, ErrMalformedRevision)
		})
	}
}

// ── PinnedRevision construction ─────────────────────────────────────────────

func TestNewPinnedRevision(t *testing.T) {
	cases := []struct {
		name    string
		family  string
		version string
		wantErr error
	}{
		{"lawful", "acp", "v1", nil},
		{"unversioned pin fails closed", "acp", "", ErrMalformedPin},
		{"empty family", "", "v1", ErrMalformedPin},
		{"family with slash", "acp/v1", "v1", ErrMalformedPin},
		{"version with slash", "acp", "v1/extra", ErrMalformedPin},
		{"whitespace version", "acp", " v1", ErrMalformedPin},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewPinnedRevision(tc.family, tc.version)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("NewPinnedRevision: %v", err)
				}
				if p.String() != tc.family+"/"+tc.version {
					t.Errorf("pin String() = %q", p.String())
				}
				return
			}
			requireTypedError(t, err, tc.wantErr)
		})
	}
}

// ── Pinned admission matrix ─────────────────────────────────────────────────

func TestAdmitPinned(t *testing.T) {
	pin := mustPin(t, "acp", "v1")
	cases := []struct {
		name      string
		presented Revision
		wantErr   error // nil = 接纳
	}{
		{"exact match", Revision{Family: "acp", Version: "v1"}, nil},
		{"family-only match, unversioned presented", Revision{Family: "acp"}, ErrPinnedMismatch},
		{"family match, wrong version", Revision{Family: "acp", Version: "v2"}, ErrPinnedMismatch},
		{"wrong family, same version", Revision{Family: "other", Version: "v1"}, ErrPinnedMismatch},
		{"garbage: zero revision", Revision{}, ErrPinnedMismatch},
		{"garbage: free text family", Revision{Family: "garbage"}, ErrPinnedMismatch},
		{"garbage: unversioned other", Revision{Family: "other"}, ErrPinnedMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AdmitPinned(pin, tc.presented)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("AdmitPinned: %v", err)
				}
				return
			}
			requireTypedError(t, err, tc.wantErr)
		})
	}
}

func TestAdmitPinned_UnversionedPinFailsClosed(t *testing.T) {
	// 绕过构造器的零值/非法 pin：结构侧必须 fail closed。
	cases := []struct {
		name string
		pin  PinnedRevision
	}{
		{"zero pin", PinnedRevision{}},
		{"unversioned pin", PinnedRevision{Family: "acp"}},
		{"empty family", PinnedRevision{Version: "v1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AdmitPinned(tc.pin, Revision{Family: "acp", Version: "v1"})
			requireTypedError(t, err, ErrMalformedPin)
		})
	}
}

// ── Migration construction matrix ───────────────────────────────────────────

func TestNewSupersedeMigration_Matrix(t *testing.T) {
	d1 := digestOf("snapshot", "acp-legacy")
	d2 := digestOf("snapshot", "acp-v1")
	dProv := digestOf("migration-authorization", "1")

	acpLegacy := Revision{Family: "acp"}
	acpV1 := Revision{Family: "acp", Version: "v1"}
	acpV2 := Revision{Family: "acp", Version: "v2"}
	otherV1 := Revision{Family: "other", Version: "v1"}

	cases := []struct {
		name       string
		fromDigest string
		toDigest   string
		from       Revision
		to         Revision
		prov       string
		wantErr    error // nil = 成功
	}{
		{"lawful acp → acp/v1", d1, d2, acpLegacy, acpV1, dProv, nil},
		{"lawful acp/v1 → acp/v2", d1, d2, acpV1, acpV2, dProv, nil},
		{"same digest", d1, d1, acpLegacy, acpV1, dProv, ErrSameDigest},
		{"unversioned target", d1, d2, acpV1, acpLegacy, dProv, ErrNotAMigration},
		{"family change: legacy → other/v1", d1, d2, acpLegacy, otherV1, dProv, ErrFamilyChanged},
		{"family change: acp/v1 → other/v1", d1, d2, acpV1, otherV1, dProv, ErrFamilyChanged},
		{"same versioned revision: ordinary supersede", d1, d2, acpV1, acpV1, dProv, ErrNotAMigration},
		{"same unversioned revision", d1, d2, acpLegacy, acpLegacy, dProv, ErrNotAMigration},
		{"missing provenance", d1, d2, acpLegacy, acpV1, "", ErrMalformedDigest},
		{"malformed provenance", d1, d2, acpLegacy, acpV1, "sha256:not-hex", ErrMalformedDigest},
		{"malformed from digest", "not-a-digest", d2, acpLegacy, acpV1, dProv, ErrMalformedDigest},
		{"malformed to digest (uppercase hex)", d1, "sha256:" + strings.Repeat("A", 64), acpLegacy, acpV1, dProv, ErrMalformedDigest},
		{"malformed to digest (short hex)", d1, "sha256:abcd", acpLegacy, acpV1, dProv, ErrMalformedDigest},
		{"malformed from revision", d1, d2, Revision{}, acpV1, dProv, ErrMalformedRevision},
		{"malformed to revision (empty family)", d1, d2, acpLegacy, Revision{Version: "v1"}, dProv, ErrMalformedRevision},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := NewSupersedeMigration(tc.fromDigest, tc.toDigest, tc.from, tc.to, tc.prov)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("NewSupersedeMigration: %v", err)
				}
				if m.FromDigest != tc.fromDigest || m.ToDigest != tc.toDigest ||
					m.FromRevision != tc.from || m.ToRevision != tc.to ||
					m.ProvenanceDigest != tc.prov {
					t.Errorf("field echo mismatch: %+v", m)
				}
				if !strings.HasPrefix(m.MigrationDigest, "sha256:") {
					t.Errorf("MigrationDigest %q missing sha256: prefix", m.MigrationDigest)
				}
				return
			}
			requireTypedError(t, err, tc.wantErr)
		})
	}
}

func TestNewSupersedeMigration_FromParse(t *testing.T) {
	// 端到端：从原始字符串解析出 revision，再走迁移构造。
	from, err := ParseRevision("acp")
	if err != nil {
		t.Fatalf("ParseRevision(acp): %v", err)
	}
	to, err := ParseRevision("acp/v1")
	if err != nil {
		t.Fatalf("ParseRevision(acp/v1): %v", err)
	}
	m := mustMigration(t, digestOf("snapshot", "1"), digestOf("snapshot", "2"), from, to, digestOf("auth"))
	if m.ToRevision != (Revision{Family: "acp", Version: "v1"}) {
		t.Errorf("ToRevision = %+v", m.ToRevision)
	}
}

// ── Migration digest echo / tamper ──────────────────────────────────────────

func TestMigration_DigestDeterministic(t *testing.T) {
	d1 := digestOf("snapshot", "1")
	d2 := digestOf("snapshot", "2")
	prov := digestOf("auth")
	from := Revision{Family: "acp"}
	to := Revision{Family: "acp", Version: "v1"}

	m1 := mustMigration(t, d1, d2, from, to, prov)
	m2 := mustMigration(t, d1, d2, from, to, prov)
	if m1.MigrationDigest != m2.MigrationDigest {
		t.Errorf("digest not deterministic: %q vs %q", m1.MigrationDigest, m2.MigrationDigest)
	}
	derived, err := m1.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	if derived != m1.MigrationDigest {
		t.Errorf("Digest() = %q, want echo %q", derived, m1.MigrationDigest)
	}
	if err := m1.Validate(); err != nil {
		t.Errorf("Validate on freshly constructed migration: %v", err)
	}
}

func TestMigration_ValidateTamper(t *testing.T) {
	base := mustMigration(t,
		digestOf("snapshot", "1"), digestOf("snapshot", "2"),
		Revision{Family: "acp"}, Revision{Family: "acp", Version: "v1"},
		digestOf("auth"))

	cases := []struct {
		name   string
		mutate func(m *SupersedeMigration)
		want   error
	}{
		{"tamper ToRevision", func(m *SupersedeMigration) { m.ToRevision = Revision{Family: "acp", Version: "v9"} }, ErrMigrationDigestMismatch},
		{"tamper FromRevision", func(m *SupersedeMigration) { m.FromRevision = Revision{Family: "acp", Version: "v0"} }, ErrMigrationDigestMismatch},
		{"tamper ProvenanceDigest", func(m *SupersedeMigration) { m.ProvenanceDigest = digestOf("forged") }, ErrMigrationDigestMismatch},
		{"tamper ToDigest", func(m *SupersedeMigration) { m.ToDigest = digestOf("forged snapshot") }, ErrMigrationDigestMismatch},
		{"blank MigrationDigest", func(m *SupersedeMigration) { m.MigrationDigest = "" }, ErrMalformedDigest},
		{"garbage MigrationDigest", func(m *SupersedeMigration) { m.MigrationDigest = "sha256:xyz" }, ErrMalformedDigest},
		{"foreign MigrationDigest", func(m *SupersedeMigration) { m.MigrationDigest = digestOf("foreign") }, ErrMigrationDigestMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := base
			tc.mutate(&m)
			requireTypedError(t, m.Validate(), tc.want)
		})
	}
}

// ── HistoryGuard ────────────────────────────────────────────────────────────

func TestHistoryGuard_Record(t *testing.T) {
	g := NewHistoryGuard()
	d := digestOf("snapshot", "1")

	if err := g.Record(d); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := g.Record(d); err != nil {
		t.Errorf("idempotent re-Record of same digest must succeed, got %v", err)
	}
	other := digestOf("snapshot", "2")
	if err := g.Record(other); err != nil {
		t.Errorf("Record of new digest must succeed, got %v", err)
	}

	requireTypedError(t, g.Record(""), ErrMalformedDigest)
	requireTypedError(t, g.Record("sha256:XYZ"), ErrMalformedDigest)
}

func TestAssertMigrationPreserves_UnknownHistory(t *testing.T) {
	d1 := digestOf("snapshot", "1")
	d2 := digestOf("snapshot", "2")
	m := mustMigration(t, d1, d2,
		Revision{Family: "acp"}, Revision{Family: "acp", Version: "v1"},
		digestOf("auth"))

	// 守护为空：FromDigest 从未冻结。
	requireTypedError(t, AssertMigrationPreserves(NewHistoryGuard(), m), ErrUnknownHistory)

	// 守护冻结过其它 digest，仍不能迁移未冻结的 FromDigest。
	g := NewHistoryGuard()
	if err := g.Record(digestOf("unrelated")); err != nil {
		t.Fatalf("Record: %v", err)
	}
	requireTypedError(t, AssertMigrationPreserves(g, m), ErrUnknownHistory)
}

func TestAssertMigrationPreserves_DigestCollision(t *testing.T) {
	d1 := digestOf("snapshot", "1")
	d2 := digestOf("snapshot", "2")
	m := mustMigration(t, d1, d2,
		Revision{Family: "acp"}, Revision{Family: "acp", Version: "v1"},
		digestOf("auth"))

	g := NewHistoryGuard()
	for _, d := range []string{d1, d2} {
		if err := g.Record(d); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	// ToDigest 已冻结：目标不是新事实。
	requireTypedError(t, AssertMigrationPreserves(g, m), ErrDigestCollision)
}

func TestAssertMigrationPreserves_FullLawfulPath(t *testing.T) {
	d1 := digestOf("snapshot", "acp-legacy")
	d2 := digestOf("snapshot", "acp-v1")
	d3 := digestOf("snapshot", "acp-v2")
	prov := digestOf("auth")

	m1 := mustMigration(t, d1, d2,
		Revision{Family: "acp"}, Revision{Family: "acp", Version: "v1"}, prov)
	m2 := mustMigration(t, d2, d3,
		Revision{Family: "acp", Version: "v1"}, Revision{Family: "acp", Version: "v2"}, prov)

	g := NewHistoryGuard()
	if err := g.Record(d1); err != nil {
		t.Fatalf("Record(d1): %v", err)
	}

	// acp → acp/v1 合法落地。
	if err := AssertMigrationPreserves(g, m1); err != nil {
		t.Fatalf("AssertMigrationPreserves(m1): %v", err)
	}
	// 判定成功不改变任何记录：重复判定依然成功（ToDigest 未被隐式冻结）。
	if err := AssertMigrationPreserves(g, m1); err != nil {
		t.Errorf("AssertMigrationPreserves must not mutate the guard, second assert got %v", err)
	}
	// 调用方显式冻结 ToDigest。
	if err := g.Record(d2); err != nil {
		t.Fatalf("Record(d2): %v", err)
	}
	// 冻结后再走同一迁移：ToDigest 已是历史 → 冲突。
	requireTypedError(t, AssertMigrationPreserves(g, m1), ErrDigestCollision)

	// 链式再迁移 acp/v1 → acp/v2：FromDigest(d2) 已冻结、ToDigest(d3) 全新，合法。
	if err := AssertMigrationPreserves(g, m2); err != nil {
		t.Fatalf("AssertMigrationPreserves(m2): %v", err)
	}
	if err := g.Record(d3); err != nil {
		t.Fatalf("Record(d3): %v", err)
	}
}

func TestAssertMigrationPreserves_ValidatesMigrationFirst(t *testing.T) {
	d1 := digestOf("snapshot", "1")
	d2 := digestOf("snapshot", "2")
	m := mustMigration(t, d1, d2,
		Revision{Family: "acp"}, Revision{Family: "acp", Version: "v1"},
		digestOf("auth"))
	// 篡改字段但保留旧 digest：即使历史状态齐全，Validate 先 fail closed。
	m.ToRevision = Revision{Family: "acp", Version: "v9"}

	g := NewHistoryGuard()
	if err := g.Record(d1); err != nil {
		t.Fatalf("Record: %v", err)
	}
	requireTypedError(t, AssertMigrationPreserves(g, m), ErrMigrationDigestMismatch)
}

func TestAssertMigrationPreserves_NilGuard(t *testing.T) {
	m := mustMigration(t,
		digestOf("snapshot", "1"), digestOf("snapshot", "2"),
		Revision{Family: "acp"}, Revision{Family: "acp", Version: "v1"},
		digestOf("auth"))
	err := AssertMigrationPreserves(nil, m)
	if err == nil {
		t.Fatal("expected nil guard to fail closed")
	}
	if !strings.HasPrefix(err.Error(), testErrPrefix) {
		t.Errorf("error %q missing %q prefix", err.Error(), testErrPrefix)
	}
}
