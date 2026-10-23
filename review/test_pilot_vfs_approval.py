"""Protocol and HTTPS+mTLS client regressions for Pilot VFS promotion."""

from __future__ import annotations

import base64
from datetime import datetime, timedelta, timezone
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import socket
import ssl
import tempfile
import threading
import unittest

from cryptography import x509
from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from cryptography.x509.oid import NameOID

import pilot_vfs_approval as approval


NOW = datetime(2026, 10, 23, 4, 0, 0, tzinfo=timezone.utc)
ARCHIVE = "a" * 64
SNAPSHOT = "b" * 64
REPORT = "c" * 64
DIFF = "d" * 64
BINDINGS = {
    "archive_sha256": ARCHIVE,
    "source_snapshot_sha256": SNAPSHOT,
    "candidate_report_sha256": REPORT,
    "reviewed_diff_sha256": DIFF,
}


def canonical(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, allow_nan=False, sort_keys=True,
                      separators=(",", ":")).encode("utf-8")


def b64u(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")


def signed_envelope(payload: dict, private_key: Ed25519PrivateKey) -> bytes:
    payload_bytes = canonical(payload)
    envelope = {"payload_b64u": b64u(payload_bytes), "signature": b64u(private_key.sign(payload_bytes))}
    return canonical(envelope)


def reviewer_public(private_key: Ed25519PrivateKey) -> bytes:
    return private_key.public_key().public_bytes(serialization.Encoding.Raw, serialization.PublicFormat.Raw)


def make_approval(key_id: str = "owner-key-1", subject: str = "l17728", sequence: int = 4,
                  approved: datetime = NOW - timedelta(minutes=10),
                  expires: datetime = NOW + timedelta(hours=23, minutes=50),
                  private_key: Ed25519PrivateKey | None = None) -> tuple[bytes, Ed25519PrivateKey]:
    private_key = private_key or Ed25519PrivateKey.generate()
    payload = {
        "schema": approval.APPROVAL_SCHEMA,
        "repository": approval.REPOSITORY,
        "purpose": approval.PROMOTION_PURPOSE,
        "archive_sha256": ARCHIVE,
        "source_snapshot_sha256": SNAPSHOT,
        "candidate_report_sha256": REPORT,
        "reviewed_diff_sha256": DIFF,
        "review_outcome": "APPROVE",
        "reviewer_subject": subject,
        "reviewer_key_id": key_id,
        "approved_at": approved.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "expires_at": expires.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "approval_sequence": sequence,
    }
    return signed_envelope(payload, private_key), private_key


def make_record(approval_bytes: bytes, reviewer_key_id: str = "owner-key-1", sequence: int = 4,
                promotion_time: datetime = NOW, service_key: Ed25519PrivateKey | None = None,
                **changes: object) -> tuple[bytes, Ed25519PrivateKey, str]:
    service_key = service_key or Ed25519PrivateKey.generate()
    promotion_id = b64u(bytes(range(32)))
    payload = {
        "schema": approval.PROMOTION_RECORD_SCHEMA,
        "promotion_id": promotion_id,
        "approval_envelope_sha256": hashlib.sha256(approval_bytes).hexdigest(),
        "repository": approval.REPOSITORY,
        "purpose": approval.PROMOTION_PURPOSE,
        "archive_sha256": ARCHIVE,
        "source_snapshot_sha256": SNAPSHOT,
        "candidate_report_sha256": REPORT,
        "reviewed_diff_sha256": DIFF,
        "reviewer_subject": "l17728",
        "reviewer_key_id": reviewer_key_id,
        "approval_sequence": sequence,
        "promotion_time": promotion_time.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "result": approval.PROMOTION_RESULT,
    }
    payload.update(changes)
    return signed_envelope(payload, service_key), service_key, promotion_id


def reviewer_store(key_id: str, key: Ed25519PrivateKey, *, subject: str = "l17728",
                   roles: frozenset[str] = frozenset({approval.REVIEWER_ROLE}), active: bool = True):
    return {key_id: approval.ReviewerKey(reviewer_public(key), subject, roles, active)}


def certificate_pair(directory: Path, common_name: str, *, ca_key: Ed25519PrivateKey,
                     ca_cert: x509.Certificate, san: x509.GeneralName) -> tuple[Path, Path]:
    key = Ed25519PrivateKey.generate()
    subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, common_name)])
    now = datetime.now(timezone.utc)
    cert = (
        x509.CertificateBuilder()
        .subject_name(subject)
        .issuer_name(ca_cert.subject)
        .public_key(key.public_key())
        .serial_number(x509.random_serial_number())
        .not_valid_before(now - timedelta(minutes=1))
        .not_valid_after(now + timedelta(days=1))
        .add_extension(x509.SubjectAlternativeName([san]), critical=False)
        .sign(ca_key, algorithm=None)
    )
    cert_path = directory / f"{common_name}.crt.pem"
    key_path = directory / f"{common_name}.key.pem"
    cert_path.write_bytes(cert.public_bytes(serialization.Encoding.PEM))
    key_path.write_bytes(key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8,
                                            serialization.NoEncryption()))
    return cert_path, key_path


class ApprovalProtocolTests(unittest.TestCase):
    def setUp(self) -> None:
        self.approval_bytes, self.reviewer_private = make_approval()
        self.reviewer_keys = reviewer_store("owner-key-1", self.reviewer_private)
        self.service_private = Ed25519PrivateKey.generate()
        self.record_bytes, _, self.promotion_id = make_record(
            self.approval_bytes, service_key=self.service_private)
        self.service_public = reviewer_public(self.service_private)

    def test_valid_single_l17728_signature_and_exact_bindings(self) -> None:
        result = approval.verify_approval(self.approval_bytes, self.reviewer_keys,
                                          promotion_time=NOW, expected_bindings=BINDINGS)
        self.assertEqual(result.payload["reviewer_subject"], "l17728")
        self.assertEqual(result.payload["approval_sequence"], 4)
        self.assertEqual(len(result.envelope_sha256), 64)

    def test_rejects_wrong_subject_key_role_revocation_and_bad_signature(self) -> None:
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(self.approval_bytes, reviewer_store("owner-key-1", self.reviewer_private, subject="other"))
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(self.approval_bytes, reviewer_store("owner-key-1", self.reviewer_private, roles=frozenset()))
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(self.approval_bytes, reviewer_store("owner-key-1", self.reviewer_private, active=False))
        wrong = reviewer_store("owner-key-1", Ed25519PrivateKey.generate())
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(self.approval_bytes, wrong)

    def test_rejects_unknown_field_bool_sequence_and_bad_interval(self) -> None:
        payload = json.loads(base64.urlsafe_b64decode(json.loads(self.approval_bytes)["payload_b64u"] + "==").decode())
        payload["unexpected"] = "no"
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(signed_envelope(payload, self.reviewer_private), self.reviewer_keys)
        payload.pop("unexpected")
        payload["approval_sequence"] = True
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(signed_envelope(payload, self.reviewer_private), self.reviewer_keys)
        payload["approval_sequence"] = 4
        payload["expires_at"] = payload["approved_at"]
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(signed_envelope(payload, self.reviewer_private), self.reviewer_keys)

    def test_enforces_approval_time_endpoints_and_24_hour_limit(self) -> None:
        expired_approval, key = make_approval(approved=NOW - timedelta(hours=25),
                                              expires=NOW - timedelta(hours=1))
        store = reviewer_store("owner-key-1", key)
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(expired_approval, store, promotion_time=NOW)
        at_expiry, key2 = make_approval(approved=NOW - timedelta(hours=24), expires=NOW)
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(at_expiry, reviewer_store("owner-key-1", key2), promotion_time=NOW)
        at_start, key_start = make_approval(approved=NOW, expires=NOW + timedelta(hours=24))
        approval.verify_approval(at_start, reviewer_store("owner-key-1", key_start), promotion_time=NOW)
        before_start, key_before = make_approval(approved=NOW + timedelta(seconds=1),
                                                 expires=NOW + timedelta(hours=24))
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(before_start, reviewer_store("owner-key-1", key_before), promotion_time=NOW)
        before_expiry, key3 = make_approval(approved=NOW - timedelta(hours=24) + timedelta(seconds=1),
                                            expires=NOW + timedelta(seconds=1))
        approval.verify_approval(before_expiry, reviewer_store("owner-key-1", key3), promotion_time=NOW)

    def test_maximum_calendar_year_uses_safe_duration_check(self) -> None:
        approved = datetime(9999, 12, 31, 0, 0, 0, tzinfo=timezone.utc)
        expires = datetime(9999, 12, 31, 1, 0, 0, tzinfo=timezone.utc)
        envelope, key = make_approval(approved=approved, expires=expires)
        approval.verify_approval(envelope, reviewer_store("owner-key-1", key))

    def test_rejects_duplicate_noncanonical_and_padded_encodings(self) -> None:
        raw = self.approval_bytes.decode()
        duplicate = raw[:-1] + ',"signature":"' + json.loads(raw)["signature"] + '"}'
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(duplicate.encode(), self.reviewer_keys)
        outer = json.loads(raw)
        outer["payload_b64u"] += "="
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(canonical(outer), self.reviewer_keys)
        reordered = "{\"signature\":" + json.dumps(json.loads(raw)["signature"]) + ",\"payload_b64u\":" + json.dumps(json.loads(raw)["payload_b64u"]) + "}"
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(reordered.encode(), self.reviewer_keys)
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval((" " + raw).encode(), self.reviewer_keys)
        payload = base64.urlsafe_b64decode(json.loads(raw)["payload_b64u"] + "==")
        duplicate_payload = payload[:-1] + b',"schema":"pilot-vfs-golden-approval-v2"}'
        duplicate_envelope = canonical({
            "payload_b64u": b64u(duplicate_payload),
            "signature": b64u(self.reviewer_private.sign(duplicate_payload)),
        })
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(duplicate_envelope, self.reviewer_keys)
        whitespace = b" " + payload
        altered_outer = {"payload_b64u": b64u(whitespace), "signature": outer.get("signature", json.loads(raw)["signature"])}
        with self.assertRaises(approval.ApprovalError):
            approval.verify_approval(canonical(altered_outer), self.reviewer_keys)

    def test_valid_promotion_record_binds_approval_and_all_artifacts(self) -> None:
        record = approval.verify_promotion_record(self.record_bytes, self.approval_bytes,
                                                  self.reviewer_keys, self.service_public,
                                                  expected_bindings=BINDINGS)
        self.assertEqual(record.payload["promotion_id"], self.promotion_id)
        self.assertEqual(record.payload["result"], "PASS_GOLDEN")

    def test_rejects_forged_or_mismatched_promotion_records(self) -> None:
        with self.assertRaises(approval.ApprovalError):
            approval.verify_promotion_record(self.record_bytes, self.approval_bytes,
                                              self.reviewer_keys, reviewer_public(Ed25519PrivateKey.generate()))
        wrong_approval, _ = make_approval(sequence=5)
        with self.assertRaises(approval.ApprovalError):
            approval.verify_promotion_record(self.record_bytes, wrong_approval,
                                              self.reviewer_keys, self.service_public)
        mismatch, service_key, _ = make_record(self.approval_bytes, service_key=self.service_private,
                                               archive_sha256="e" * 64)
        with self.assertRaises(approval.ApprovalError):
            approval.verify_promotion_record(mismatch, self.approval_bytes, self.reviewer_keys,
                                              reviewer_public(service_key))
        expired, _, _ = make_record(self.approval_bytes, service_key=self.service_private,
                                    promotion_time=NOW + timedelta(hours=24))
        with self.assertRaises(approval.ApprovalError):
            approval.verify_promotion_record(expired, self.approval_bytes, self.reviewer_keys,
                                              self.service_public)

    def test_reviewer_and_service_signing_keys_must_be_distinct(self) -> None:
        record, _, _ = make_record(self.approval_bytes, service_key=self.reviewer_private)
        with self.assertRaises(approval.ApprovalError):
            approval.verify_promotion_record(record, self.approval_bytes, self.reviewer_keys,
                                              reviewer_public(self.reviewer_private))

    def test_service_key_must_not_reuse_any_key_in_reviewer_trust_store(self) -> None:
        record, _, _ = make_record(self.approval_bytes, service_key=self.service_private)
        expanded = dict(self.reviewer_keys)
        expanded["other-reviewer"] = approval.ReviewerKey(self.service_public, "other", frozenset())
        with self.assertRaises(approval.ApprovalError):
            approval.verify_promotion_record(record, self.approval_bytes, expanded, self.service_public)

    def test_record_requires_closed_schema_and_canonical_payload(self) -> None:
        record, _, _ = make_record(self.approval_bytes, service_key=self.service_private,
                                   reviewer_key_id="different-key")
        with self.assertRaises(approval.ApprovalError):
            approval.verify_promotion_record(record, self.approval_bytes, self.reviewer_keys,
                                              self.service_public)
        outer = json.loads(self.record_bytes)
        payload = base64.urlsafe_b64decode(outer["payload_b64u"] + "==")
        noncanonical = b" " + payload
        bad = canonical({"payload_b64u": b64u(noncanonical), "signature": outer["signature"]})
        with self.assertRaises(approval.ApprovalError):
            approval.verify_promotion_record(bad, self.approval_bytes, self.reviewer_keys,
                                              self.service_public)
        payload_raw = base64.urlsafe_b64decode(outer["payload_b64u"] + "==")
        duplicate_payload = payload_raw[:-1] + b',"result":"PASS_GOLDEN"}'
        duplicate_record = canonical({
            "payload_b64u": b64u(duplicate_payload),
            "signature": b64u(self.service_private.sign(duplicate_payload)),
        })
        with self.assertRaises(approval.ApprovalError):
            approval.verify_promotion_record(duplicate_record, self.approval_bytes,
                                              self.reviewer_keys, self.service_public)
        unknown_payload = json.loads(payload_raw)
        unknown_payload["unexpected"] = "no"
        unknown_record = signed_envelope(unknown_payload, self.service_private)
        with self.assertRaises(approval.ApprovalError):
            approval.verify_promotion_record(unknown_record, self.approval_bytes,
                                              self.reviewer_keys, self.service_public)


class PromotionServiceClientTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.tls_dir = tempfile.TemporaryDirectory()
        cls.directory = Path(cls.tls_dir.name)
        cls.ca_key = Ed25519PrivateKey.generate()
        ca_subject = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "Pilot Test CA")])
        now = datetime.now(timezone.utc)
        cls.ca_cert = (
            x509.CertificateBuilder().subject_name(ca_subject).issuer_name(ca_subject)
            .public_key(cls.ca_key.public_key()).serial_number(x509.random_serial_number())
            .not_valid_before(now - timedelta(minutes=1)).not_valid_after(now + timedelta(days=1))
            .add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
            .sign(cls.ca_key, algorithm=None)
        )
        cls.ca_path = cls.directory / "ca.pem"
        cls.ca_path.write_bytes(cls.ca_cert.public_bytes(serialization.Encoding.PEM))
        cls.server_cert, cls.server_key = certificate_pair(cls.directory, "server", ca_key=cls.ca_key,
                                                            ca_cert=cls.ca_cert,
                                                            san=x509.DNSName("localhost"))
        cls.client_cert, cls.client_key = certificate_pair(cls.directory, "l17728", ca_key=cls.ca_key,
                                                            ca_cert=cls.ca_cert,
                                                            san=x509.UniformResourceIdentifier("urn:pilot:subject:l17728"))
        cls.other_cert, cls.other_key = certificate_pair(cls.directory, "other", ca_key=cls.ca_key,
                                                          ca_cert=cls.ca_cert,
                                                          san=x509.UniformResourceIdentifier("urn:pilot:subject:other"))

    @classmethod
    def tearDownClass(cls) -> None:
        cls.tls_dir.cleanup()

    def setUp(self) -> None:
        self.approval_bytes, self.reviewer_private = make_approval()
        self.reviewer_keys = reviewer_store("owner-key-1", self.reviewer_private)
        self.service_private = Ed25519PrivateKey.generate()
        self.record_bytes, _, self.promotion_id = make_record(self.approval_bytes,
                                                              service_key=self.service_private)
        self.calls: list[tuple[str, str, bytes | None]] = []
        self.ledger_mutations = 0
        self.lookup_status = 200
        self.response_content_type = "application/json"
        self.drop_post_response = False
        self.post_status = 201
        self.oversize_post_error = False
        calls = self.calls
        mutations = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, _format: str, *_args: object) -> None:
                return

            def do_POST(self) -> None:
                length = int(self.headers.get("Content-Length", "0"))
                body = self.rfile.read(length)
                calls.append(("POST", self.path, body))
                if not self.authorized_client():
                    return
                if self.path != "/v1/promotions" or body != self_outer.approval_bytes:
                    self.send_error(400)
                    return
                if mutations.post_status != 201:
                    self.send_response(mutations.post_status)
                    if mutations.oversize_post_error:
                        self.send_header("Content-Length", str(approval.MAX_RESPONSE_BYTES + 10))
                    self.end_headers()
                    if mutations.oversize_post_error:
                        self.wfile.write(b"x" * (approval.MAX_RESPONSE_BYTES + 10))
                    return
                if mutations.ledger_mutations:
                    self.send_response(409)
                    self.end_headers()
                    return
                mutations.ledger_mutations += 1
                self.send_response(201)
                self.send_header("Content-Type", mutations.response_content_type)
                if mutations.drop_post_response:
                    self.send_header("Content-Length", "65535")
                self.end_headers()
                if mutations.drop_post_response:
                    self.wfile.write(self_outer.record_bytes)
                    self.wfile.flush()
                    self.connection.shutdown(socket.SHUT_RDWR)
                    self.connection.close()
                    return
                self.wfile.write(self_outer.record_bytes)

            def do_GET(self) -> None:
                calls.append(("GET", self.path, None))
                if not self.authorized_client():
                    return
                if mutations.lookup_status != 200:
                    self.send_response(mutations.lookup_status)
                    self.end_headers()
                    return
                self.send_response(200)
                self.send_header("Content-Type", mutations.response_content_type)
                self.end_headers()
                self.wfile.write(self_outer.record_bytes)

            def authorized_client(self) -> bool:
                sans = self.connection.getpeercert().get("subjectAltName", ())
                if ("URI", "urn:pilot:subject:l17728") in sans:
                    return True
                self.send_response(403)
                self.end_headers()
                return False

        self_outer = self
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        server_context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        server_context.minimum_version = ssl.TLSVersion.TLSv1_2
        server_context.load_cert_chain(str(self.server_cert), str(self.server_key))
        server_context.verify_mode = ssl.CERT_REQUIRED
        server_context.load_verify_locations(cafile=str(self.ca_path))
        self.server.socket = server_context.wrap_socket(self.server.socket, server_side=True)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.client_context = approval.PromotionServiceClient.tls_context(
            str(self.ca_path), str(self.client_cert), str(self.client_key))
        self.client = approval.PromotionServiceClient(
            f"https://localhost:{self.server.server_port}", self.client_context,
            self.reviewer_keys, reviewer_public(self.service_private))

    def tearDown(self) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=3)

    def test_live_mtls_post_then_read_only_get(self) -> None:
        result = self.client.promote(self.approval_bytes, expected_bindings=BINDINGS)
        self.assertEqual(result.payload["promotion_id"], self.promotion_id)
        self.assertEqual(self.ledger_mutations, 1)
        self.assertEqual(self.calls[0], ("POST", "/v1/promotions", self.approval_bytes))
        before = self.ledger_mutations
        looked_up = self.client.lookup(self.promotion_id, self.approval_bytes,
                                       expected_bindings=BINDINGS)
        self.assertEqual(looked_up.payload["promotion_id"], self.promotion_id)
        self.assertEqual(self.calls[-1], ("GET", f"/v1/promotions/{self.promotion_id}", None))
        self.assertEqual(self.ledger_mutations, before)

    def test_v1_and_multisignature_shapes_are_rejected_before_network_write(self) -> None:
        envelope = json.loads(self.approval_bytes)
        payload_raw = base64.urlsafe_b64decode(envelope["payload_b64u"] + "==")
        payload = json.loads(payload_raw)
        payload["schema"] = "pilot-vfs-golden-approval-v1"
        legacy_raw = canonical(payload)
        legacy_signers = (self.reviewer_private, Ed25519PrivateKey.generate())
        legacy = canonical({
            "payload_b64u": b64u(legacy_raw),
            "signatures": [
                {"key_id": key_id, "signature": b64u(key.sign(legacy_raw))}
                for key_id, key in (("reviewer-a", legacy_signers[0]),
                                    ("reviewer-b", legacy_signers[1]))
            ],
        })
        signature_array = dict(envelope)
        signature_array["signature"] = [signature_array["signature"]]
        extra_signatures = dict(envelope)
        extra_signatures["signatures"] = [extra_signatures["signature"]]
        for bad in (legacy, canonical(signature_array), canonical(extra_signatures)):
            with self.subTest(envelope=bad[:30]), self.assertRaises(approval.ApprovalError):
                self.client.promote(bad)
        self.assertEqual(self.calls, [])
        self.assertEqual(self.ledger_mutations, 0)

    def test_mutually_authenticated_tls_rejects_missing_client_certificate(self) -> None:
        no_client_cert = ssl.create_default_context(cafile=str(self.ca_path))
        client = approval.PromotionServiceClient(
            f"https://localhost:{self.server.server_port}", no_client_cert,
            self.reviewer_keys, reviewer_public(self.service_private))
        with self.assertRaises(approval.PromotionServiceError):
            client.lookup(self.promotion_id, self.approval_bytes)
        with self.assertRaises(approval.PromotionServiceError):
            client.promote(self.approval_bytes)
        self.assertEqual(self.ledger_mutations, 0)

    def test_mutually_authenticated_tls_rejects_wrong_client_identity(self) -> None:
        wrong_context = approval.PromotionServiceClient.tls_context(
            str(self.ca_path), str(self.other_cert), str(self.other_key))
        client = approval.PromotionServiceClient(
            f"https://localhost:{self.server.server_port}", wrong_context,
            self.reviewer_keys, reviewer_public(self.service_private))
        with self.assertRaises(approval.PromotionServiceError):
            client.lookup(self.promotion_id, self.approval_bytes)
        with self.assertRaises(approval.PromotionServiceError):
            client.promote(self.approval_bytes)
        self.assertEqual(self.ledger_mutations, 0)

    def test_weakening_shared_tls_context_after_initialization_is_rejected(self) -> None:
        self.client_context.check_hostname = False
        self.client_context.verify_mode = ssl.CERT_NONE
        with self.assertRaises(approval.ApprovalError):
            self.client.lookup(self.promotion_id, self.approval_bytes)
        with self.assertRaises(approval.ApprovalError):
            self.client.promote(self.approval_bytes)
        self.assertEqual(self.calls, [])

    def test_repeat_post_does_not_create_second_promotion(self) -> None:
        self.client.promote(self.approval_bytes)
        with self.assertRaises(approval.PromotionServiceError):
            self.client.promote(self.approval_bytes)
        self.assertEqual(self.ledger_mutations, 1)

    def test_not_found_and_unavailable_lookup_never_authorize(self) -> None:
        for status in (404, 503):
            with self.subTest(status=status):
                self.lookup_status = status
                with self.assertRaises(approval.PromotionServiceError):
                    self.client.lookup(self.promotion_id, self.approval_bytes)
                self.assertEqual(self.ledger_mutations, 0)

    def test_lookup_rejects_a_valid_record_for_another_id(self) -> None:
        other_id = b64u(b"z" * 32)
        self.record_bytes, _, _ = make_record(self.approval_bytes, promotion_id=other_id,
                                              service_key=self.service_private)
        with self.assertRaises(approval.ApprovalError):
            self.client.lookup(self.promotion_id, self.approval_bytes)

    def test_invalid_success_responses_fail_closed(self) -> None:
        self.response_content_type = "text/plain"
        with self.assertRaises(approval.PromotionServiceError) as content_type_error:
            self.client.promote(self.approval_bytes)
        self.assertTrue(content_type_error.exception.outcome_unknown)
        self.assertEqual(self.ledger_mutations, 1)

        self.ledger_mutations = 0
        self.response_content_type = "application/json"
        self.record_bytes = b"{" + b" " * approval.MAX_RESPONSE_BYTES
        with self.assertRaises(approval.PromotionServiceError) as size_error:
            self.client.promote(self.approval_bytes)
        self.assertTrue(size_error.exception.outcome_unknown)
        self.assertEqual(self.ledger_mutations, 1)

        self.ledger_mutations = 0
        self.response_content_type = "text/plain"
        with self.assertRaises(approval.PromotionServiceError):
            self.client.lookup(self.promotion_id, self.approval_bytes)
        self.assertEqual(self.ledger_mutations, 0)

    def test_lost_post_response_reports_ambiguous_commit_without_retry(self) -> None:
        self.drop_post_response = True
        with self.assertRaises(approval.PromotionServiceError) as error:
            self.client.promote(self.approval_bytes)
        self.assertTrue(error.exception.outcome_unknown)
        self.assertEqual(self.ledger_mutations, 1)
        self.assertEqual(len([call for call in self.calls if call[0] == "POST"]), 1)

    def test_oversize_503_is_marked_outcome_unknown(self) -> None:
        self.post_status = 503
        self.oversize_post_error = True
        with self.assertRaises(approval.PromotionServiceError) as error:
            self.client.promote(self.approval_bytes)
        self.assertTrue(error.exception.outcome_unknown)
        self.assertEqual(self.ledger_mutations, 0)

    def test_service_requires_tls_context_and_https_origin(self) -> None:
        no_verify = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        no_verify.check_hostname = False
        no_verify.verify_mode = ssl.CERT_NONE
        with self.assertRaises(approval.ApprovalError):
            approval.PromotionServiceClient("https://localhost", no_verify,
                                            self.reviewer_keys, reviewer_public(self.service_private))
        with self.assertRaises(approval.ApprovalError):
            approval.PromotionServiceClient("http://localhost", self.client_context,
                                            self.reviewer_keys, reviewer_public(self.service_private))
        with self.assertRaises(approval.ApprovalError):
            approval.PromotionServiceClient("https://user:pass@localhost", self.client_context,
                                            self.reviewer_keys, reviewer_public(self.service_private))
        for malformed in ("https://localhost:invalid", "https://localhost:0", "https://localhost:65536"):
            with self.subTest(url=malformed), self.assertRaises(approval.ApprovalError):
                approval.PromotionServiceClient(malformed, self.client_context,
                                                self.reviewer_keys, reviewer_public(self.service_private))
        for timeout in (float("nan"), float("inf"), True, 0, 31, "1"):
            with self.subTest(timeout=timeout), self.assertRaises(approval.ApprovalError):
                approval.PromotionServiceClient("https://localhost", self.client_context,
                                                self.reviewer_keys, reviewer_public(self.service_private),
                                                timeout_seconds=timeout)


if __name__ == "__main__":
    unittest.main()
