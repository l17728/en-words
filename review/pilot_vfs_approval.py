"""Strict client-side verification for Pilot VFS golden approvals.

This module never creates reviewer signatures or authorizes a candidate run.
Production keys and the mutually-authenticated service connection are supplied
by the caller from protected, out-of-band configuration.
"""

from __future__ import annotations

import base64
import binascii
from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
import hashlib
import http.client
import json
import math
import re
import ssl
from typing import Any, Mapping
from urllib.parse import quote, urlsplit

try:
    from cryptography.exceptions import InvalidSignature
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
except ImportError as exc:  # pragma: no cover - exercised by isolated packaging checks
    raise RuntimeError("cryptography with Ed25519 support is required") from exc


APPROVAL_SCHEMA = "pilot-vfs-golden-approval-v2"
PROMOTION_RECORD_SCHEMA = "pilot-vfs-golden-promotion-record-v1"
RECOVERY_FENCE_SCHEMA = "pilot-vfs-promotion-recovery-fence-v1"
REPOSITORY = "l17728/en-words"
PROMOTION_PURPOSE = "PILOT_VFS_GOLDEN_PROMOTION"
REVIEWER_SUBJECT = "l17728"
REVIEWER_ROLE = "PILOT_GOLDEN_REVIEWER"
PROMOTION_RESULT = "PASS_GOLDEN"
MAX_RESPONSE_BYTES = 64 * 1024

APPROVAL_FIELDS = frozenset({
    "schema", "repository", "purpose", "archive_sha256", "source_snapshot_sha256",
    "candidate_report_sha256", "reviewed_diff_sha256", "review_outcome",
    "reviewer_subject", "reviewer_key_id", "approved_at", "expires_at",
    "approval_sequence",
})
RECORD_FIELDS = frozenset({
    "schema", "promotion_id", "approval_envelope_sha256", "repository", "purpose",
    "archive_sha256", "source_snapshot_sha256", "candidate_report_sha256",
    "reviewed_diff_sha256", "reviewer_subject", "reviewer_key_id",
    "approval_sequence", "promotion_time", "result",
})
RECOVERY_FENCE_FIELDS = frozenset({
    "schema", "approval_envelope_sha256", "repository", "purpose", "reviewer_subject",
    "reviewer_key_id", "approval_sequence", "reconciled_at", "result", "fence_id",
})
ENVELOPE_FIELDS = frozenset({"payload_b64u", "signature"})
HEX256_RE = re.compile(r"[0-9a-f]{64}\Z", re.ASCII)
KEY_ID_RE = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}\Z", re.ASCII)
UTC_SECOND_RE = re.compile(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z\Z", re.ASCII)
BASE64URL_RE = re.compile(r"[A-Za-z0-9_-]+\Z", re.ASCII)


class ApprovalError(ValueError):
    """A malformed, unauthorized, mismatched, or unverified approval record."""


class PromotionServiceError(RuntimeError):
    """The promotion service refused or could not complete a request."""

    def __init__(self, message: str, *, outcome_unknown: bool = False) -> None:
        super().__init__(message)
        self.outcome_unknown = outcome_unknown


@dataclass(frozen=True)
class ReviewerKey:
    public_key: bytes
    subject: str
    roles: frozenset[str]
    active: bool = True
    valid_from: datetime | None = None
    valid_until: datetime | None = None
    revoked_at: datetime | None = None
    reviewer_role_valid_from: datetime | None = None
    reviewer_role_valid_until: datetime | None = None


@dataclass(frozen=True)
class VerifiedApproval:
    payload: Mapping[str, Any]
    envelope_sha256: str


@dataclass(frozen=True)
class VerifiedPromotion:
    payload: Mapping[str, Any]
    envelope_sha256: str


@dataclass(frozen=True)
class VerifiedRecovery:
    state: str
    payload: Mapping[str, Any]
    envelope_sha256: str


@dataclass(frozen=True)
class _ServiceResponse:
    status: int
    body: bytes


def _reject_duplicate_pairs(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ApprovalError("duplicate JSON object key")
        result[key] = value
    return result


def _parse_object(raw: bytes, *, canonical: bool) -> dict[str, Any]:
    if not isinstance(raw, bytes) or not raw or len(raw) > MAX_RESPONSE_BYTES:
        raise ApprovalError("JSON document is empty or exceeds the size limit")
    try:
        value = json.loads(
            raw.decode("utf-8", errors="strict"),
            object_pairs_hook=_reject_duplicate_pairs,
            parse_constant=lambda _: (_ for _ in ()).throw(ApprovalError("non-finite JSON number")),
        )
    except ApprovalError:
        raise
    except (UnicodeDecodeError, json.JSONDecodeError, RecursionError) as exc:
        raise ApprovalError("invalid UTF-8 JSON document") from exc
    if not isinstance(value, dict):
        raise ApprovalError("JSON document must be an object")
    if canonical and _canonical_json(value) != raw:
        raise ApprovalError("JSON payload is not RFC 8785 canonical")
    return value


def _canonical_json(value: Any) -> bytes:
    """Serialize the restricted string/integer schema using RFC 8785 bytes.

    Protocol payload fields contain only ASCII strings, objects, and exact
    integers; floats and arbitrary Unicode are rejected by schema validation.
    Under that closed value domain, sorted compact JSON is byte-identical to
    JCS property ordering and string escaping.
    """
    try:
        return json.dumps(value, ensure_ascii=False, allow_nan=False, sort_keys=True,
                          separators=(",", ":")).encode("utf-8")
    except (TypeError, ValueError, UnicodeEncodeError) as exc:
        raise ApprovalError("payload cannot be canonically serialized") from exc


def _decode_b64u(value: Any, *, expected_bytes: int | None = None) -> bytes:
    if not isinstance(value, str) or not value or "=" in value or not BASE64URL_RE.fullmatch(value):
        raise ApprovalError("invalid unpadded base64url field")
    try:
        decoded = base64.b64decode(value + "=" * ((4 - len(value) % 4) % 4), altchars=b"-_", validate=True)
    except (binascii.Error, ValueError) as exc:
        raise ApprovalError("invalid base64url encoding") from exc
    if base64.urlsafe_b64encode(decoded).rstrip(b"=").decode("ascii") != value:
        raise ApprovalError("noncanonical base64url encoding")
    if expected_bytes is not None and len(decoded) != expected_bytes:
        raise ApprovalError("base64url field has an invalid decoded length")
    return decoded


def _timestamp(value: Any, field: str) -> datetime:
    if not isinstance(value, str) or not UTC_SECOND_RE.fullmatch(value):
        raise ApprovalError(f"{field} must use UTC second precision")
    try:
        return datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)
    except ValueError as exc:
        raise ApprovalError(f"{field} is not a valid Gregorian timestamp") from exc


def _uint(value: Any, field: str) -> int:
    if type(value) is not int or not 1 <= value <= 9_007_199_254_740_991:
        raise ApprovalError(f"{field} must be a positive RFC 8785 exact integer")
    return value


def _digest(value: Any, field: str) -> str:
    if not isinstance(value, str) or not HEX256_RE.fullmatch(value):
        raise ApprovalError(f"{field} must be 64 lowercase hexadecimal characters")
    return value


def _exact_fields(value: Mapping[str, Any], fields: frozenset[str], label: str) -> None:
    if set(value) != fields:
        raise ApprovalError(f"{label} has missing or unknown fields")


def _envelope(raw: bytes) -> tuple[dict[str, Any], bytes, bytes]:
    outer = _parse_object(raw, canonical=False)
    if _canonical_json(outer) != raw:
        raise ApprovalError("JSON envelope is not RFC 8785 canonical")
    _exact_fields(outer, ENVELOPE_FIELDS, "envelope")
    payload_raw = _decode_b64u(outer["payload_b64u"])
    signature = _decode_b64u(outer["signature"], expected_bytes=64)
    payload = _parse_object(payload_raw, canonical=True)
    return outer, payload_raw, signature


def _verify_signature(public_key: bytes, signature: bytes, payload: bytes) -> None:
    if not isinstance(public_key, bytes) or len(public_key) != 32:
        raise ApprovalError("trusted Ed25519 public key is invalid")
    try:
        Ed25519PublicKey.from_public_bytes(public_key).verify(signature, payload)
    except (InvalidSignature, ValueError) as exc:
        raise ApprovalError("Ed25519 signature verification failed") from exc


def _approval_shape(
    payload: Mapping[str, Any],
    keys: Mapping[str, ReviewerKey],
    *,
    historical: bool,
) -> ReviewerKey:
    _exact_fields(payload, APPROVAL_FIELDS, "approval payload")
    constants = {
        "schema": APPROVAL_SCHEMA,
        "repository": REPOSITORY,
        "purpose": PROMOTION_PURPOSE,
        "review_outcome": "APPROVE",
        "reviewer_subject": REVIEWER_SUBJECT,
    }
    for field, expected in constants.items():
        if payload.get(field) != expected or not isinstance(payload.get(field), str):
            raise ApprovalError(f"approval {field} is not authorized")
    for field in ("archive_sha256", "source_snapshot_sha256", "candidate_report_sha256", "reviewed_diff_sha256"):
        _digest(payload.get(field), field)
    key_id = payload.get("reviewer_key_id")
    if not isinstance(key_id, str) or not KEY_ID_RE.fullmatch(key_id):
        raise ApprovalError("reviewer_key_id is invalid")
    key = keys.get(key_id)
    if key is None or key.subject != REVIEWER_SUBJECT or REVIEWER_ROLE not in key.roles:
        raise ApprovalError("reviewer key is not authorized")
    _uint(payload.get("approval_sequence"), "approval_sequence")
    approved_at = _timestamp(payload.get("approved_at"), "approved_at")
    expires_at = _timestamp(payload.get("expires_at"), "expires_at")
    for value, label in (
        (key.valid_from, "reviewer key valid_from"),
        (key.valid_until, "reviewer key valid_until"),
        (key.revoked_at, "reviewer key revoked_at"),
        (key.reviewer_role_valid_from, "reviewer role valid_from"),
        (key.reviewer_role_valid_until, "reviewer role valid_until"),
    ):
        if value is not None and (not isinstance(value, datetime) or value.tzinfo is None
                                  or value.utcoffset() != timedelta(0)):
            raise ApprovalError(f"{label} must be timezone-aware UTC")
    if key.valid_from is not None and approved_at < key.valid_from:
        raise ApprovalError("reviewer key was not yet valid when approval was signed")
    if key.valid_until is not None and approved_at >= key.valid_until:
        raise ApprovalError("reviewer key had expired when approval was signed")
    if key.revoked_at is not None and approved_at >= key.revoked_at:
        raise ApprovalError("reviewer key was revoked when approval was signed")
    if key.reviewer_role_valid_from is not None and approved_at < key.reviewer_role_valid_from:
        raise ApprovalError("reviewer role was not yet valid when approval was signed")
    if key.reviewer_role_valid_until is not None and approved_at >= key.reviewer_role_valid_until:
        raise ApprovalError("reviewer role had ended when approval was signed")
    if historical:
        if not key.active and key.revoked_at is None and key.valid_until is None:
            raise ApprovalError("inactive reviewer key has no trusted historical status")
    elif not key.active:
        raise ApprovalError("reviewer key is not currently active")
    if not approved_at < expires_at or expires_at - approved_at > timedelta(hours=24):
        raise ApprovalError("approval validity interval is invalid")
    return key


def verify_approval(
    raw_envelope: bytes,
    reviewer_keys: Mapping[str, ReviewerKey],
    *,
    promotion_time: datetime | None = None,
    expected_bindings: Mapping[str, str] | None = None,
    historical_key_status: bool = False,
) -> VerifiedApproval:
    """Verify one l17728 approval and optionally bind it to promotion time/artifacts."""
    _, payload_raw, signature = _envelope(raw_envelope)
    payload = _parse_object(payload_raw, canonical=True)
    reviewer = _approval_shape(payload, reviewer_keys, historical=historical_key_status)
    _verify_signature(reviewer.public_key, signature, payload_raw)
    if expected_bindings:
        for field, expected in expected_bindings.items():
            if field not in ("archive_sha256", "source_snapshot_sha256", "candidate_report_sha256", "reviewed_diff_sha256"):
                raise ApprovalError("unknown expected artifact binding")
            if payload[field] != expected:
                raise ApprovalError("approval artifact binding mismatch")
    if promotion_time is not None:
        if promotion_time.tzinfo is None or promotion_time.utcoffset() != timedelta(0):
            raise ApprovalError("promotion_time must be timezone-aware UTC")
        approved_at = _timestamp(payload["approved_at"], "approved_at")
        expires_at = _timestamp(payload["expires_at"], "expires_at")
        if not approved_at <= promotion_time < expires_at:
            raise ApprovalError("approval is outside its signed validity window")
    return VerifiedApproval(payload, hashlib.sha256(raw_envelope).hexdigest())


def verify_promotion_record(
    raw_record_envelope: bytes,
    raw_approval_envelope: bytes,
    reviewer_keys: Mapping[str, ReviewerKey],
    service_public_key: bytes,
    *,
    expected_bindings: Mapping[str, str] | None = None,
) -> VerifiedPromotion:
    """Verify a service-authenticated record against the original l17728 approval."""
    _, payload_raw, signature = _envelope(raw_record_envelope)
    payload = _parse_object(payload_raw, canonical=True)
    _exact_fields(payload, RECORD_FIELDS, "promotion record")
    constants = {
        "schema": PROMOTION_RECORD_SCHEMA,
        "repository": REPOSITORY,
        "purpose": PROMOTION_PURPOSE,
        "reviewer_subject": REVIEWER_SUBJECT,
        "result": PROMOTION_RESULT,
    }
    for field, expected in constants.items():
        if payload.get(field) != expected or not isinstance(payload.get(field), str):
            raise ApprovalError(f"promotion record {field} is invalid")
    promotion_id = _decode_b64u(payload.get("promotion_id"), expected_bytes=32)
    if base64.urlsafe_b64encode(promotion_id).rstrip(b"=").decode("ascii") != payload["promotion_id"]:
        raise ApprovalError("promotion_id is noncanonical")
    _digest(payload.get("approval_envelope_sha256"), "approval_envelope_sha256")
    if payload["approval_envelope_sha256"] != hashlib.sha256(raw_approval_envelope).hexdigest():
        raise ApprovalError("promotion record approval envelope mismatch")
    for field in ("archive_sha256", "source_snapshot_sha256", "candidate_report_sha256", "reviewed_diff_sha256"):
        _digest(payload.get(field), field)
    key_id = payload.get("reviewer_key_id")
    if not isinstance(key_id, str) or not KEY_ID_RE.fullmatch(key_id):
        raise ApprovalError("promotion record reviewer_key_id is invalid")
    sequence = _uint(payload.get("approval_sequence"), "approval_sequence")
    promotion_time = _timestamp(payload.get("promotion_time"), "promotion_time")
    _verify_signature(service_public_key, signature, payload_raw)

    approval = verify_approval(raw_approval_envelope, reviewer_keys, promotion_time=promotion_time,
                               historical_key_status=True)
    approved = approval.payload
    if any(key.public_key == service_public_key for key in reviewer_keys.values()):
        raise ApprovalError("reviewer and promotion service keys must be distinct")
    for field in ("archive_sha256", "source_snapshot_sha256", "candidate_report_sha256", "reviewed_diff_sha256"):
        if payload[field] != approved[field]:
            raise ApprovalError("promotion record artifact binding mismatch")
    if payload["reviewer_subject"] != approved["reviewer_subject"] or key_id != approved["reviewer_key_id"] or sequence != approved["approval_sequence"]:
        raise ApprovalError("promotion record reviewer or sequence mismatch")
    if expected_bindings:
        for field, expected in expected_bindings.items():
            if field not in ("archive_sha256", "source_snapshot_sha256", "candidate_report_sha256", "reviewed_diff_sha256"):
                raise ApprovalError("unknown expected artifact binding")
            if payload[field] != expected:
                raise ApprovalError("promotion record artifact binding mismatch")
    return VerifiedPromotion(payload, hashlib.sha256(raw_record_envelope).hexdigest())


def verify_recovery_fence(
    raw_fence_envelope: bytes,
    raw_approval_envelope: bytes,
    reviewer_keys: Mapping[str, ReviewerKey],
    service_public_key: bytes,
    *,
    expected_bindings: Mapping[str, str] | None = None,
) -> VerifiedRecovery:
    """Verify a signed, durable fence for a historically authorized approval."""
    _, payload_raw, signature = _envelope(raw_fence_envelope)
    payload = _parse_object(payload_raw, canonical=True)
    _exact_fields(payload, RECOVERY_FENCE_FIELDS, "recovery fence")
    constants = {
        "schema": RECOVERY_FENCE_SCHEMA,
        "repository": REPOSITORY,
        "purpose": PROMOTION_PURPOSE,
        "reviewer_subject": REVIEWER_SUBJECT,
        "result": "FENCED_NOT_COMMITTED",
    }
    for field, expected in constants.items():
        if payload.get(field) != expected or not isinstance(payload.get(field), str):
            raise ApprovalError(f"recovery fence {field} is invalid")
    _digest(payload.get("approval_envelope_sha256"), "approval_envelope_sha256")
    expected_digest = hashlib.sha256(raw_approval_envelope).hexdigest()
    if payload["approval_envelope_sha256"] != expected_digest:
        raise ApprovalError("recovery fence approval digest mismatch")
    key_id = payload.get("reviewer_key_id")
    if not isinstance(key_id, str) or not KEY_ID_RE.fullmatch(key_id):
        raise ApprovalError("recovery fence reviewer_key_id is invalid")
    sequence = _uint(payload.get("approval_sequence"), "approval_sequence")
    reconciled_at = _timestamp(payload.get("reconciled_at"), "reconciled_at")
    _decode_b64u(payload.get("fence_id"), expected_bytes=32)
    _verify_signature(service_public_key, signature, payload_raw)

    approval = verify_approval(raw_approval_envelope, reviewer_keys,
                               expected_bindings=expected_bindings,
                               historical_key_status=True)
    if reconciled_at < _timestamp(approval.payload["approved_at"], "approved_at"):
        raise ApprovalError("recovery fence predates its approval")
    if any(key.public_key == service_public_key for key in reviewer_keys.values()):
        raise ApprovalError("reviewer and promotion service keys must be distinct")
    if (payload["reviewer_subject"] != approval.payload["reviewer_subject"]
            or key_id != approval.payload["reviewer_key_id"]
            or sequence != approval.payload["approval_sequence"]):
        raise ApprovalError("recovery fence reviewer or sequence mismatch")
    return VerifiedRecovery("FENCED_NOT_COMMITTED", payload,
                            hashlib.sha256(raw_fence_envelope).hexdigest())


class PromotionServiceClient:
    """Small HTTPS+mTLS client; signed records are still verified locally."""

    def __init__(
        self,
        base_url: str,
        tls_context: ssl.SSLContext,
        reviewer_keys: Mapping[str, ReviewerKey],
        service_public_key: bytes,
        *,
        timeout_seconds: float = 10.0,
    ) -> None:
        try:
            parts = urlsplit(base_url)
            parsed_port = parts.port
        except (TypeError, ValueError) as exc:
            raise ApprovalError("promotion service URL is malformed") from exc
        if (parts.scheme != "https" or not parts.hostname or parts.username or parts.password
                or parts.query or parts.fragment or parts.path not in ("", "/")):
            raise ApprovalError("promotion service URL must be an HTTPS origin")
        port = 443 if parsed_port is None else parsed_port
        if not 1 <= port <= 65535:
            raise ApprovalError("promotion service port is invalid")
        if (tls_context.verify_mode != ssl.CERT_REQUIRED or not tls_context.check_hostname
                or tls_context.minimum_version < ssl.TLSVersion.TLSv1_2):
            raise ApprovalError("promotion TLS context must verify server certificate and hostname")
        if (isinstance(timeout_seconds, bool) or not isinstance(timeout_seconds, (int, float))
                or not math.isfinite(timeout_seconds) or timeout_seconds <= 0 or timeout_seconds > 30):
            raise ApprovalError("promotion service timeout is outside the allowed range")
        self._host = parts.hostname
        self._port = port
        self._tls = tls_context
        self._reviewer_keys = reviewer_keys
        self._service_key = service_public_key
        self._timeout = timeout_seconds

    @staticmethod
    def tls_context(ca_file: str, client_cert: str, client_key: str) -> ssl.SSLContext:
        context = ssl.create_default_context(ssl.Purpose.SERVER_AUTH, cafile=ca_file)
        context.minimum_version = ssl.TLSVersion.TLSv1_2
        context.check_hostname = True
        context.verify_mode = ssl.CERT_REQUIRED
        context.load_cert_chain(certfile=client_cert, keyfile=client_key)
        return context

    def promote(self, approval_envelope: bytes, *, expected_bindings: Mapping[str, str] | None = None) -> VerifiedPromotion:
        # Validate canonical form, reviewer role, signature and expected artifact
        # binding before transmitting anything. Trusted time is checked by service.
        verify_approval(approval_envelope, self._reviewer_keys, expected_bindings=expected_bindings)
        response = self._request("POST", "/v1/promotions", approval_envelope)
        if response.status != 201:
            raise PromotionServiceError("promotion service did not accept the approval",
                                        outcome_unknown=response.status in (200, 201) or response.status >= 500)
        try:
            return verify_promotion_record(response.body, approval_envelope, self._reviewer_keys,
                                           self._service_key, expected_bindings=expected_bindings)
        except ApprovalError as exc:
            raise PromotionServiceError(
                "promotion may have committed but its record could not be verified; reconcile the authoritative ledger",
                outcome_unknown=True,
            ) from exc

    def lookup(self, promotion_id: str, approval_envelope: bytes, *, expected_bindings: Mapping[str, str] | None = None) -> VerifiedPromotion:
        _decode_b64u(promotion_id, expected_bytes=32)
        response = self._request("GET", "/v1/promotions/" + quote(promotion_id, safe=""), None)
        if response.status != 200:
            raise PromotionServiceError("promotion service did not return an authorized record")
        verified = verify_promotion_record(response.body, approval_envelope, self._reviewer_keys,
                                           self._service_key, expected_bindings=expected_bindings)
        if verified.payload["promotion_id"] != promotion_id:
            raise ApprovalError("promotion service returned a different record ID")
        return verified

    def recover(
        self,
        approval_envelope: bytes,
        *,
        expected_bindings: Mapping[str, str] | None = None,
    ) -> VerifiedRecovery:
        """Resolve an ambiguous POST using the reviewed reconcile-and-fence route.

        This method never retries promotion. Only a locally verified promotion
        record or durable fence is conclusive; every other response remains
        outcome_unknown.
        """
        verify_approval(approval_envelope, self._reviewer_keys,
                        expected_bindings=expected_bindings,
                        historical_key_status=True)
        digest = hashlib.sha256(approval_envelope).hexdigest()
        path = "/v1/promotions/reconcile/" + quote(digest, safe="")
        try:
            response = self._request("POST", path, approval_envelope)
            if response.status != 200:
                raise PromotionServiceError("reconciliation did not establish the promotion outcome",
                                            outcome_unknown=True)
            _, payload_raw, _ = _envelope(response.body)
            payload = _parse_object(payload_raw, canonical=True)
            schema = payload.get("schema")
            if schema == PROMOTION_RECORD_SCHEMA:
                promotion = verify_promotion_record(
                    response.body, approval_envelope, self._reviewer_keys,
                    self._service_key, expected_bindings=expected_bindings,
                )
                return VerifiedRecovery("PROMOTED", promotion.payload, promotion.envelope_sha256)
            if schema == RECOVERY_FENCE_SCHEMA:
                return verify_recovery_fence(
                    response.body, approval_envelope, self._reviewer_keys,
                    self._service_key, expected_bindings=expected_bindings,
                )
            raise ApprovalError("reconciliation response schema is unsupported")
        except ApprovalError as exc:
            raise PromotionServiceError("reconciliation response could not be verified",
                                        outcome_unknown=True) from exc
        except PromotionServiceError as exc:
            if exc.outcome_unknown:
                raise
            raise PromotionServiceError("reconciliation response did not establish a terminal outcome",
                                        outcome_unknown=True) from exc

    def _request(self, method: str, path: str, body: bytes | None) -> _ServiceResponse:
        if (self._tls.verify_mode != ssl.CERT_REQUIRED or not self._tls.check_hostname
                or self._tls.minimum_version < ssl.TLSVersion.TLSv1_2):
            raise ApprovalError("promotion TLS context was weakened after client initialization")
        connection = http.client.HTTPSConnection(self._host, self._port, context=self._tls,
                                                 timeout=self._timeout)
        response = None
        try:
            headers = {"Accept": "application/json"}
            if body is not None:
                headers["Content-Type"] = "application/json"
            connection.request(method, path, body=body, headers=headers)
            response = connection.getresponse()
            content_type = response.getheader("Content-Type", "").split(";", 1)[0].strip().lower()
            data = response.read(MAX_RESPONSE_BYTES + 1)
            if len(data) > MAX_RESPONSE_BYTES:
                raise PromotionServiceError("promotion service response exceeded the size limit",
                                            outcome_unknown=method == "POST" and
                                            (response.status in (200, 201) or response.status >= 500))
            if response.length is not None and response.length > 0:
                raise PromotionServiceError("promotion service response body was incomplete",
                                            outcome_unknown=method == "POST" and
                                            (response.status in (200, 201) or response.status >= 500))
            if response.status in (200, 201) and content_type != "application/json":
                raise PromotionServiceError("promotion service response had an invalid content type",
                                            outcome_unknown=method == "POST" and
                                            (response.status in (200, 201) or response.status >= 500))
            return _ServiceResponse(response.status, data)
        except (OSError, ssl.SSLError, http.client.HTTPException) as exc:
            raise PromotionServiceError("promotion service request failed",
                                        outcome_unknown=method == "POST") from exc
        finally:
            try:
                if response is not None:
                    response.close()
            except Exception:
                # Cleanup must not replace the protocol result or its ambiguity classification.
                pass
            finally:
                try:
                    connection.close()
                except Exception:
                    # A failed close cannot make a previously received response authoritative.
                    pass
