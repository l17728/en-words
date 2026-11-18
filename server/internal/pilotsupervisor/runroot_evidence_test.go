//go:build pilot_e2e

package pilotsupervisor

import (
	"testing"
)

func TestRunRootEvidenceVectors(t *testing.T) {
	epoch := "00000000000000000000000000000000"
	requestID := "11111111111111111111111111111111"
	process := []any{int64(11), int64(101), int64(2), int64(3), int64(4), int64(5)}
	cases := []struct {
		name, kind, expectedSource, expectedPayload, expectedEvidence string
		identity, payload                                             any
	}{
		{
			name: "created", kind: "run-root-created-v1",
			expectedSource:   "3a43ce88726a8a1bef11dabf6111ef2171612c8aaaa3df3aeccbe390c6d0ee32",
			expectedPayload:  "5f26ebeaf37b7f1d363047c4b90f3d9311264d7fbf7dbf991117c07824071dbe",
			expectedEvidence: "5d8bf14f6a1bf0f5a28279e7cde9c0a7bfdf97500043eceba0ea47171a9c1ee7",
			identity:         []any{int64(1), int64(2), int64(3), int64(4), int64(5), int64(6)},
			payload:          []any{epoch, requestID, int64(1), process, int64(1001), int64(100), int64(448), true, true, int64(0), int64(100), int64(456), true, true, true, true},
		},
		{
			name: "collision", kind: "run-root-create-collision-v1",
			expectedSource:   "714068527ca6bc87eff8ec61c461fba06904e1d4e79c7b487b6b03826f960e32",
			expectedPayload:  "0b7b8bb9355147979000170e79105afad4385ac4bf931c775712909be04c85d9",
			expectedEvidence: "bb97e789877d9e655df140464fe88212ace7ea806f991dd48552b759a930a50c",
			identity:         []any{int64(1), int64(2), int64(3)},
			payload:          []any{epoch, requestID, int64(1), process, "mkdirat", "EEXIST", false},
		},
		{
			name: "absent", kind: "run-root-create-effect-absent-v1",
			expectedSource:   "01e431cc7f11995117ea2168ce4307ae9b672b4be77109a68d73b954ff2a3633",
			expectedPayload:  "ec663d2bc8cbf81dacbda7734f83001fa26ed5d61846b78bdeaf2c2929171c79",
			expectedEvidence: "91fbc3666cd5185aa28195faed1e4eb6214fdf6006a4b5526cff3bb0a5ab6551",
			identity:         []any{int64(1), int64(2), int64(3), []any{"absent", []any{int64(1), int64(2), int64(3)}, runRootBasenameDigest(epoch), "ENOENT"}},
			payload:          []any{epoch, requestID, int64(1), process, "mkdirat", "EACCES", true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			item, err := buildRunRootEvidenceItem(tc.kind, tc.identity, tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			if got := runRootDigestHex(item.SourceIdentityDigest); got != tc.expectedSource {
				t.Fatalf("source identity digest = %s, want %s", got, tc.expectedSource)
			}
			if got := runRootDigestHex(item.PayloadDigest); got != tc.expectedPayload {
				t.Fatalf("evidence payload digest = %s, want %s", got, tc.expectedPayload)
			}
			if got := runRootDigestHex(item.EvidenceDigest); got != tc.expectedEvidence {
				t.Fatalf("evidence digest = %s, want %s", got, tc.expectedEvidence)
			}
		})
	}
}

func TestRunRootEvidenceDigestDomainAndSchemaAreBound(t *testing.T) {
	identity := []any{int64(1), int64(2), int64(3)}
	kind := "run-root-create-collision-v1"
	got, err := runRootSourceIdentityDigest(kind, identity)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := runRootSourceIdentityDigest(kind, []any{int64(1), int64(2), int64(4)})
	if err != nil {
		t.Fatal(err)
	}
	if got == changed {
		t.Fatal("identity mutation preserved digest")
	}
	if _, err := runRootSourceIdentityDigest("unregistered-v1", identity); err == nil {
		t.Fatal("unregistered source kind accepted")
	}
	if _, err := runRootPayloadDigest(kind, []any{map[string]any{"unexpected": 1}}); err == nil {
		t.Fatal("map evidence payload accepted")
	}
	if _, err := runRootPayloadDigest(kind, []any{float64(1)}); err == nil {
		t.Fatal("floating-point evidence payload accepted")
	}
	if _, err := buildRunRootEvidenceItem("unregistered-v1", identity, []any{}); err == nil {
		t.Fatal("unregistered evidence source kind accepted")
	}
}

func TestRunRootEvidenceRejectsSchemaDrift(t *testing.T) {
	process := []any{int64(11), int64(101), int64(2), int64(3), int64(4), int64(5)}
	created := []any{"00000000000000000000000000000000", "11111111111111111111111111111111", int64(1), process, int64(1001), int64(100), int64(0700), true, true, int64(0), int64(100), int64(0710), true, true, true, true}
	collision := []any{created[0], created[1], int64(1), process, "mkdirat", "EEXIST", false}
	absent := []any{created[0], created[1], int64(1), process, "mkdirat", "EACCES", true}
	cases := []struct {
		name, kind string
		identity   any
		payload    any
	}{
		{"created-identity-too-short", "run-root-created-v1", []any{int64(1), int64(2), int64(3)}, created},
		{"created-identity-wrong-field-type", "run-root-created-v1", []any{int64(1), int64(2), "3", int64(4), int64(5), int64(6)}, created},
		{"created-payload-too-short", "run-root-created-v1", []any{int64(1), int64(2), int64(3), int64(4), int64(5), int64(6)}, created[:15]},
		{"created-wrong-mode", "run-root-created-v1", []any{int64(1), int64(2), int64(3), int64(4), int64(5), int64(6)}, append(append([]any{}, created[:6]...), append([]any{int64(1)}, created[7:]...)...)},
		{"collision-target-opened", "run-root-create-collision-v1", []any{int64(1), int64(2), int64(3)}, append(append([]any{}, collision[:6]...), true)},
		{"collision-wrong-errno", "run-root-create-collision-v1", []any{int64(1), int64(2), int64(3)}, append(append([]any{}, collision[:5]...), "EACCES", false)},
		{"absent-unlisted-errno", "run-root-create-effect-absent-v1", []any{int64(1), int64(2), int64(3), []any{"absent", []any{int64(1), int64(2), int64(3)}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "ENOENT"}}, append(append([]any{}, absent[:5]...), "EIO", true)},
		{"absent-malformed-witness", "run-root-create-effect-absent-v1", []any{int64(1), int64(2), int64(3), []any{"absent", []any{int64(1), int64(2)}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "ENOENT"}}, absent},
		{"absent-parent-mismatch", "run-root-create-effect-absent-v1", []any{int64(1), int64(2), int64(3), []any{"absent", []any{int64(4), int64(5), int64(6)}, runRootBasenameDigest(created[0].(string)), "ENOENT"}}, absent},
		{"absent-basename-epoch-mismatch", "run-root-create-effect-absent-v1", []any{int64(1), int64(2), int64(3), []any{"absent", []any{int64(1), int64(2), int64(3)}, runRootBasenameDigest("22222222222222222222222222222222"), "ENOENT"}}, absent},
		{"nil-typed-slice", "run-root-create-collision-v1", []any{int64(1), int64(2), int64(3)}, []any(nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildRunRootEvidenceItem(tc.kind, tc.identity, tc.payload); err == nil {
				t.Fatal("invalid source identity/payload schema accepted")
			}
		})
	}
	invalidRequest := append([]any{}, collision...)
	invalidRequest[1] = "request-\xff"
	if _, err := buildRunRootEvidenceItem("run-root-create-collision-v1", []any{int64(1), int64(2), int64(3)}, invalidRequest); err == nil {
		t.Fatal("invalid UTF-8 request ID accepted")
	}
}
