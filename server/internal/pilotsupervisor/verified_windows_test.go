//go:build pilot_e2e && windows

package pilotsupervisor

import (
	"errors"
	"fmt"
	"testing"
)

func TestWindowsBindingOpenErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		in   error
		want error
	}{
		{name: "missing_binding_is_identity_change", in: errVerifiedMemberInvalid, want: errVerifiedIdentityChanged},
		{name: "access_or_query_failure_is_io", in: errVerifiedIOFailed, want: errVerifiedIOFailed},
		{name: "policy_failure_is_preserved", in: errVerifiedPolicyMismatch, want: errVerifiedPolicyMismatch},
		{name: "unknown_native_error_is_io", in: fmt.Errorf("native failure"), want: errVerifiedIOFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyWindowsBindingOpenError(test.in); !errors.Is(got, test.want) {
				t.Fatalf("classifyWindowsBindingOpenError(%v) = %v, want errors.Is(_, %v)", test.in, got, test.want)
			}
		})
	}
}

func TestWindowsMissingWorkerSIDIsConfigurationFailure(t *testing.T) {
	err := verifyWindowsSecurity(0, verifiedDirectory, "")
	if !errors.Is(err, errVerifiedConfigurationMissing) {
		t.Fatalf("verifyWindowsSecurity with no pinned worker SID = %v, want configuration missing", err)
	}
	if got := verifiedErrorClass(err); got != "verified_configuration_missing" {
		t.Fatalf("configuration error class = %q", got)
	}
}
