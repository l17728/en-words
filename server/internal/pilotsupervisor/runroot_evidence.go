//go:build pilot_e2e

package pilotsupervisor

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"sync"
	"unicode/utf8"

	"github.com/fxamacker/cbor/v2"
)

var (
	errRunRootEvidenceInvalid = errors.New("pilot run-root evidence invalid")
	runRootCBORMode           cbor.EncMode
	runRootCBORModeOnce       sync.Once
	runRootCBORModeErr        error
)

// Run-root receipt digests use RFC 8949 Core Deterministic Encoding. Each
// source kind is validated against its fixed schema before encoding.
func encodeRunRootCBOR(value any) ([]byte, error) {
	if !isRunRootCBORValue(reflect.ValueOf(value)) {
		return nil, errRunRootEvidenceInvalid
	}
	runRootCBORModeOnce.Do(func() {
		runRootCBORMode, runRootCBORModeErr = cbor.CoreDetEncOptions().EncMode()
	})
	if runRootCBORModeErr != nil {
		return nil, errRunRootEvidenceInvalid
	}
	encoded, err := runRootCBORMode.Marshal(value)
	if err != nil {
		return nil, errRunRootEvidenceInvalid
	}
	return encoded, nil
}

func runRootSourceIdentityDigest(sourceKind string, platformIdentity any) ([sha256.Size]byte, error) {
	if !validRunRootSourceKind(sourceKind) || !validRunRootSourceIdentity(sourceKind, platformIdentity) {
		return [sha256.Size]byte{}, errRunRootEvidenceInvalid
	}
	encoded, err := encodeRunRootCBOR([]any{sourceKind, platformIdentity})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return hashRunRootDomain("en-words-evidence-source-v1\x00", encoded), nil
}

func runRootPayloadDigest(sourceKind string, payload any) ([sha256.Size]byte, error) {
	if !validRunRootSourcePayload(sourceKind, payload) {
		return [sha256.Size]byte{}, errRunRootEvidenceInvalid
	}
	encoded, err := encodeRunRootCBOR(payload)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return hashRunRootDomain("en-words-"+sourceKind+"\x00", encoded), nil
}

func validRunRootSourceIdentity(kind string, value any) bool {
	fields, ok := value.([]any)
	if !ok {
		return false
	}
	switch kind {
	case "run-root-created-v1":
		return len(fields) == 6 && allPositiveRunRootIntegers(fields...)
	case "run-root-create-collision-v1":
		return len(fields) == 3 && allPositiveRunRootIntegers(fields...)
	case "run-root-create-effect-absent-v1":
		if len(fields) != 4 || !allPositiveRunRootIntegers(fields[:3]...) || fields[3] == nil {
			return false
		}
		witness, ok := fields[3].([]any)
		if !ok || len(witness) != 4 || witness[0] != "absent" || witness[3] != "ENOENT" || !isLowerHexString(witness[2], 64) {
			return false
		}
		parent, ok := witness[1].([]any)
		return ok && len(parent) == 3 && allPositiveRunRootIntegers(parent...)
	default:
		return false
	}
}

func validRunRootSourcePayload(kind string, value any) bool {
	fields, ok := value.([]any)
	if !ok || len(fields) == 0 || !validRunEpoch(fields[0]) || len(fields) < 2 || !validRunRootRequestID(fields[1]) {
		return false
	}
	switch kind {
	case "run-root-created-v1":
		if len(fields) != 16 || !positiveRunRootInteger(fields[2]) || !validRunRootProcessIdentity(fields[3]) {
			return false
		}
		return nonnegativeRunRootInteger(fields[4]) && nonnegativeRunRootInteger(fields[5]) &&
			fields[6] == int64(0700) && fields[7] == true && fields[8] == true &&
			nonnegativeRunRootInteger(fields[9]) && nonnegativeRunRootInteger(fields[10]) &&
			fields[11] == int64(0710) && fields[12] == true && fields[13] == true &&
			fields[14] == true && fields[15] == true
	case "run-root-create-collision-v1":
		return len(fields) == 7 && positiveRunRootInteger(fields[2]) && validRunRootProcessIdentity(fields[3]) &&
			fields[4] == "mkdirat" && fields[5] == "EEXIST" && fields[6] == false
	case "run-root-create-effect-absent-v1":
		if len(fields) != 7 || !positiveRunRootInteger(fields[2]) || !validRunRootProcessIdentity(fields[3]) ||
			fields[4] != "mkdirat" || fields[6] != true {
			return false
		}
		errnoClass, ok := fields[5].(string)
		return ok && validRunRootErrnoClass(errnoClass)
	default:
		return false
	}
}

func validRunRootProcessIdentity(value any) bool {
	fields, ok := value.([]any)
	return ok && len(fields) == 6 && allPositiveRunRootIntegers(fields...)
}

func allPositiveRunRootIntegers(values ...any) bool {
	for _, value := range values {
		if !positiveRunRootInteger(value) {
			return false
		}
	}
	return true
}

func positiveRunRootInteger(value any) bool {
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return false
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() > 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint() > 0
	default:
		return false
	}
}

func nonnegativeRunRootInteger(value any) bool {
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return false
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() >= 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	default:
		return false
	}
}

func validRunEpoch(value any) bool {
	s, ok := value.(string)
	return ok && isLowerHexString(s, 32)
}

func validRunRootRequestID(value any) bool {
	s, ok := value.(string)
	return ok && utf8.ValidString(s) && identifier.MatchString(s)
}

func isLowerHexString(value any, length int) bool {
	s, ok := value.(string)
	if !ok || len(s) != length {
		return false
	}
	for _, c := range []byte(s) {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func validRunRootErrnoClass(value string) bool {
	switch value {
	case "EACCES", "EPERM", "EROFS", "ENOSPC", "EDQUOT", "EMLINK", "ENOMEM",
		"ENOENT", "ENOTDIR", "ELOOP", "EXDEV", "ENAMETOOLONG", "EINVAL":
		return true
	default:
		return false
	}
}

type runRootEvidenceItem struct {
	SourceIdentityDigest [sha256.Size]byte
	PayloadDigest        [sha256.Size]byte
	EvidenceDigest       [sha256.Size]byte
}

// buildRunRootEvidenceItem is the only supported composition boundary: it
// validates both typed schemas together before binding their digests.
func buildRunRootEvidenceItem(sourceKind string, platformIdentity, payload any) (runRootEvidenceItem, error) {
	var item runRootEvidenceItem
	if !validRunRootSourceKind(sourceKind) || !validRunRootSourceIdentity(sourceKind, platformIdentity) || !validRunRootSourcePayload(sourceKind, payload) {
		return item, errRunRootEvidenceInvalid
	}
	if sourceKind == "run-root-create-effect-absent-v1" && !runRootAbsenceWitnessMatches(platformIdentity, payload) {
		return item, errRunRootEvidenceInvalid
	}
	var err error
	item.SourceIdentityDigest, err = runRootSourceIdentityDigest(sourceKind, platformIdentity)
	if err != nil {
		return runRootEvidenceItem{}, err
	}
	item.PayloadDigest, err = runRootPayloadDigest(sourceKind, payload)
	if err != nil {
		return runRootEvidenceItem{}, err
	}
	// The SDD defines digest fields as lowercase hexadecimal text in the
	// deterministic-CBOR evidence item, not CBOR byte strings.
	encoded, err := encodeRunRootCBOR([]any{sourceKind, runRootDigestHex(item.SourceIdentityDigest), runRootDigestHex(item.PayloadDigest)})
	if err != nil {
		return runRootEvidenceItem{}, err
	}
	item.EvidenceDigest = hashRunRootDomain("en-words-evidence-item-v1\x00", encoded)
	return item, nil
}

func runRootAbsenceWitnessMatches(platformIdentity, payload any) bool {
	identity := platformIdentity.([]any)
	witness := identity[3].([]any)
	fields := payload.([]any)
	epoch := fields[0].(string)
	outerParent, err1 := encodeRunRootCBOR(identity[:3])
	witnessParent, err2 := encodeRunRootCBOR(witness[1])
	return err1 == nil && err2 == nil &&
		string(outerParent) == string(witnessParent) &&
		witness[2] == runRootBasenameDigest(epoch)
}

func isRunRootCBORValue(value reflect.Value) bool {
	if !value.IsValid() {
		return false
	}
	switch value.Kind() {
	case reflect.Interface:
		return !value.IsNil() && isRunRootCBORValue(value.Elem())
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return false
		}
		for i := 0; i < value.Len(); i++ {
			if !isRunRootCBORValue(value.Index(i)) {
				return false
			}
		}
		return true
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	default:
		return false
	}
}

func hashRunRootDomain(domain string, body []byte) [sha256.Size]byte {
	h := sha256.New()
	_, _ = h.Write([]byte(domain))
	_, _ = h.Write(body)
	var digest [sha256.Size]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

func validRunRootSourceKind(value string) bool {
	switch value {
	case "run-root-created-v1", "run-root-create-collision-v1", "run-root-create-effect-absent-v1":
		return true
	default:
		return false
	}
}

func runRootDigestHex(value [sha256.Size]byte) string { return hex.EncodeToString(value[:]) }

func runRootBasenameDigest(name string) string {
	return runRootDigestHex(hashRunRootDomain("en-words-basename-v1\x00", []byte(name)))
}
