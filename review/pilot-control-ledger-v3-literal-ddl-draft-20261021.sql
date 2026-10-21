-- DESIGN DRAFT ONLY. Not loaded by production code. Requires independent review.
-- SQLite minimum: 3.37 (STRICT tables). Connection contract: foreign_keys=ON,
-- recursive_triggers=ON, trusted_schema=OFF, journal_mode=WAL.

CREATE TABLE run_root_ledger_meta (
  singleton INTEGER NOT NULL PRIMARY KEY CHECK(singleton=1),
  schema_version INTEGER NOT NULL CHECK(schema_version=3)
) STRICT;

CREATE TABLE pilot_control_generations (
  generation BLOB NOT NULL PRIMARY KEY CHECK(typeof(generation)='blob' AND length(generation)=8 AND generation<>zeroblob(8)),
  launch_nonce TEXT NOT NULL UNIQUE CHECK(typeof(launch_nonce)='text' AND length(CAST(launch_nonce AS BLOB))=32 AND length(launch_nonce)=32 AND launch_nonce NOT GLOB '*[^0-9a-f]*'),
  mode TEXT NOT NULL CHECK(mode IN ('normal','recovery')),
  normal_run_epoch BLOB,
  target_run_epoch BLOB,
  target_generation BLOB,
  target_launch_nonce TEXT,
  consumed_at INTEGER NOT NULL CHECK(consumed_at>=0),
  UNIQUE(generation,launch_nonce,normal_run_epoch,mode),
  CHECK((mode='normal' AND typeof(normal_run_epoch)='blob' AND length(normal_run_epoch)=16 AND target_run_epoch IS NULL AND target_generation IS NULL AND target_launch_nonce IS NULL)
     OR (mode='recovery' AND normal_run_epoch IS NULL AND typeof(target_run_epoch)='blob' AND length(target_run_epoch)=16 AND typeof(target_generation)='blob' AND length(target_generation)=8 AND typeof(target_launch_nonce)='text' AND length(CAST(target_launch_nonce AS BLOB))=32 AND length(target_launch_nonce)=32 AND target_launch_nonce NOT GLOB '*[^0-9a-f]*'))
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_launcher_journal_anchor (
  singleton_id INTEGER NOT NULL PRIMARY KEY CHECK(singleton_id=1),
  journal_id BLOB NOT NULL UNIQUE CHECK(typeof(journal_id)='blob' AND length(journal_id)=16 AND journal_id<>zeroblob(16)),
  restore_generation BLOB NOT NULL CHECK(typeof(restore_generation)='blob' AND length(restore_generation)=8 AND restore_generation<>zeroblob(8)),
  last_sequence INTEGER NOT NULL CHECK(last_sequence>=0),
  last_record_hash BLOB NOT NULL CHECK(typeof(last_record_hash)='blob' AND length(last_record_hash)=32),
  updated_at INTEGER NOT NULL CHECK(updated_at>=0),
  CHECK((last_sequence=0 AND last_record_hash=zeroblob(32))
     OR (last_sequence>0 AND last_record_hash<>zeroblob(32)))
) STRICT, WITHOUT ROWID;

CREATE TRIGGER pilot_launcher_journal_anchor_no_delete
BEFORE DELETE ON pilot_launcher_journal_anchor
BEGIN
  SELECT RAISE(ABORT,'launcher journal anchor cannot be deleted');
END;

CREATE TRIGGER pilot_launcher_journal_anchor_monotonic
BEFORE UPDATE ON pilot_launcher_journal_anchor
WHEN NEW.singleton_id<>OLD.singleton_id OR NEW.journal_id<>OLD.journal_id OR NEW.restore_generation<>OLD.restore_generation
  OR NEW.last_sequence<>OLD.last_sequence+1 OR NEW.last_record_hash=OLD.last_record_hash
  OR NEW.updated_at<OLD.updated_at
BEGIN
  SELECT RAISE(ABORT,'launcher journal anchor must advance exactly once');
END;

CREATE TABLE pilot_principal_aliases (
  tenant_id BLOB NOT NULL CHECK(typeof(tenant_id)='blob' AND length(tenant_id) BETWEEN 1 AND 256),
  identity_key_version INTEGER NOT NULL CHECK(identity_key_version BETWEEN 1 AND 65535),
  identity_digest BLOB NOT NULL CHECK(typeof(identity_digest)='blob' AND length(identity_digest)=32),
  principal_ref BLOB NOT NULL CHECK(typeof(principal_ref)='blob' AND length(principal_ref)=32),
  canonical_key_version INTEGER NOT NULL CHECK(canonical_key_version BETWEEN 1 AND 65535),
  created_at INTEGER NOT NULL CHECK(created_at>=0),
  PRIMARY KEY(tenant_id,identity_key_version,identity_digest),
  UNIQUE(tenant_id,principal_ref,identity_key_version)
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_authorization_state (
  tenant_id BLOB NOT NULL CHECK(typeof(tenant_id)='blob' AND length(tenant_id) BETWEEN 1 AND 256),
  principal_ref BLOB NOT NULL CHECK(typeof(principal_ref)='blob' AND length(principal_ref)=32),
  authz_revision BLOB NOT NULL CHECK(typeof(authz_revision)='blob' AND length(authz_revision)=8 AND authz_revision<>zeroblob(8)),
  authz_snapshot_digest BLOB NOT NULL CHECK(typeof(authz_snapshot_digest)='blob' AND length(authz_snapshot_digest)=32),
  role TEXT NOT NULL CHECK(role IN ('student','teacher','admin')),
  account_state TEXT NOT NULL CHECK(account_state IN ('active','disabled')),
  updated_at INTEGER NOT NULL CHECK(updated_at>=0),
  PRIMARY KEY(tenant_id,principal_ref)
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_session_fences (
  tenant_id BLOB NOT NULL,
  principal_ref BLOB NOT NULL CHECK(typeof(principal_ref)='blob' AND length(principal_ref)=32),
  session_key_hash BLOB NOT NULL CHECK(typeof(session_key_hash)='blob' AND length(session_key_hash)=32),
  session_revision BLOB NOT NULL CHECK(typeof(session_revision)='blob' AND length(session_revision)=8 AND session_revision<>zeroblob(8)),
  session_state TEXT NOT NULL CHECK(session_state IN ('active','revoked')),
  expires_at INTEGER NOT NULL CHECK(expires_at>=0),
  updated_at INTEGER NOT NULL CHECK(updated_at>=0),
  PRIMARY KEY(tenant_id,principal_ref,session_key_hash),
  FOREIGN KEY(tenant_id,principal_ref) REFERENCES pilot_authorization_state(tenant_id,principal_ref) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_fixture_grants (
  tenant_id BLOB NOT NULL,
  principal_ref BLOB NOT NULL CHECK(typeof(principal_ref)='blob' AND length(principal_ref)=32),
  fixture_id TEXT NOT NULL CHECK(typeof(fixture_id)='text' AND length(CAST(fixture_id AS BLOB)) BETWEEN 1 AND 64 AND fixture_id GLOB '[a-z0-9]*' AND fixture_id NOT GLOB '*[^a-z0-9._-]*'),
  grant_revision BLOB NOT NULL CHECK(typeof(grant_revision)='blob' AND length(grant_revision)=8 AND grant_revision<>zeroblob(8)),
  allowed INTEGER NOT NULL CHECK(allowed IN (0,1)),
  updated_at INTEGER NOT NULL CHECK(updated_at>=0),
  PRIMARY KEY(tenant_id,principal_ref,fixture_id),
  FOREIGN KEY(tenant_id,principal_ref) REFERENCES pilot_authorization_state(tenant_id,principal_ref) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_authz_update_receipts (
  request_id BLOB NOT NULL PRIMARY KEY CHECK(typeof(request_id)='blob' AND length(request_id)=16),
  tenant_id BLOB NOT NULL,
  principal_ref BLOB NOT NULL CHECK(typeof(principal_ref)='blob' AND length(principal_ref)=32),
  target_kind TEXT NOT NULL CHECK(target_kind IN ('account','session')),
  target_key BLOB NOT NULL CHECK(typeof(target_key)='blob' AND length(target_key)=32),
  revision BLOB NOT NULL CHECK(typeof(revision)='blob' AND length(revision)=8 AND revision<>zeroblob(8)),
  update_digest BLOB NOT NULL CHECK(typeof(update_digest)='blob' AND length(update_digest)=32),
  committed_at INTEGER NOT NULL CHECK(committed_at>=0),
  UNIQUE(tenant_id,principal_ref,target_kind,target_key,revision),
  CHECK(target_kind<>'account' OR target_key=zeroblob(32)),
  FOREIGN KEY(tenant_id,principal_ref) REFERENCES pilot_authorization_state(tenant_id,principal_ref) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_bootstrap_handoffs (
  generation BLOB NOT NULL PRIMARY KEY CHECK(typeof(generation)='blob' AND length(generation)=8),
  launch_nonce TEXT NOT NULL CHECK(length(CAST(launch_nonce AS BLOB))=32 AND length(launch_nonce)=32 AND launch_nonce NOT GLOB '*[^0-9a-f]*'),
  run_epoch BLOB NOT NULL UNIQUE CHECK(typeof(run_epoch)='blob' AND length(run_epoch)=16),
  control_mode TEXT NOT NULL DEFAULT 'normal' CHECK(control_mode='normal'),
  gateway_pid INTEGER NOT NULL CHECK(gateway_pid>0),
  gateway_uid INTEGER NOT NULL CHECK(gateway_uid>0),
  gateway_start_ticks BLOB NOT NULL CHECK(typeof(gateway_start_ticks)='blob' AND length(gateway_start_ticks)=8 AND gateway_start_ticks<>zeroblob(8)),
  gateway_pidns_device BLOB NOT NULL CHECK(typeof(gateway_pidns_device)='blob' AND length(gateway_pidns_device)=8 AND gateway_pidns_device<>zeroblob(8)),
  gateway_pidns_inode BLOB NOT NULL CHECK(typeof(gateway_pidns_inode)='blob' AND length(gateway_pidns_inode)=8 AND gateway_pidns_inode<>zeroblob(8)),
  gateway_cgroup_mount_id BLOB NOT NULL CHECK(typeof(gateway_cgroup_mount_id)='blob' AND length(gateway_cgroup_mount_id)=8 AND gateway_cgroup_mount_id<>zeroblob(8)),
  gateway_cgroup_device BLOB NOT NULL CHECK(typeof(gateway_cgroup_device)='blob' AND length(gateway_cgroup_device)=8 AND gateway_cgroup_device<>zeroblob(8)),
  gateway_cgroup_inode BLOB NOT NULL CHECK(typeof(gateway_cgroup_inode)='blob' AND length(gateway_cgroup_inode)=8 AND gateway_cgroup_inode<>zeroblob(8)),
  handed_off_at INTEGER NOT NULL CHECK(handed_off_at>=0),
  UNIQUE(generation,run_epoch),
  UNIQUE(generation,run_epoch,gateway_pid,gateway_uid,gateway_start_ticks,gateway_pidns_device,gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode),
  UNIQUE(generation,run_epoch,launch_nonce,gateway_pid,gateway_uid,gateway_start_ticks,gateway_pidns_device,gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode),
  FOREIGN KEY(generation,launch_nonce,run_epoch,control_mode) REFERENCES pilot_control_generations(generation,launch_nonce,normal_run_epoch,mode) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_run_lifecycle (
  generation BLOB NOT NULL PRIMARY KEY CHECK(typeof(generation)='blob' AND length(generation)=8),
  run_epoch BLOB NOT NULL UNIQUE CHECK(typeof(run_epoch)='blob' AND length(run_epoch)=16),
  lifecycle_state TEXT NOT NULL CHECK(lifecycle_state IN ('bootstrapped','logger_registered','logger_ready','reaper_ready','registered','active','terminating','unregistered','containment_empty','terminal')),
  run_root_operation_id BLOB CHECK(run_root_operation_id IS NULL OR (typeof(run_root_operation_id)='blob' AND length(run_root_operation_id)=16)),
  close_operation_id BLOB CHECK(close_operation_id IS NULL OR (typeof(close_operation_id)='blob' AND length(close_operation_id)=16)),
  close_key_hash BLOB CHECK(close_key_hash IS NULL OR (typeof(close_key_hash)='blob' AND length(close_key_hash)=32)),
  close_payload_digest BLOB CHECK(close_payload_digest IS NULL OR (typeof(close_payload_digest)='blob' AND length(close_payload_digest)=32)),
  terminal_result TEXT CHECK(terminal_result IS NULL OR terminal_result IN ('abandoned','closed_without_root')),
  updated_at INTEGER NOT NULL CHECK(updated_at>=0),
  CHECK((close_operation_id IS NULL AND close_key_hash IS NULL AND close_payload_digest IS NULL) OR (close_operation_id IS NOT NULL AND close_key_hash IS NOT NULL AND close_payload_digest IS NOT NULL)),
  CHECK((lifecycle_state='terminal' AND terminal_result IS NOT NULL) OR (lifecycle_state<>'terminal' AND terminal_result IS NULL)),
  UNIQUE(generation,run_epoch),
  FOREIGN KEY(generation,run_epoch) REFERENCES pilot_bootstrap_handoffs(generation,run_epoch) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE TABLE run_epoch_allocations (
  run_epoch BLOB NOT NULL PRIMARY KEY CHECK(typeof(run_epoch)='blob' AND length(run_epoch)=16),
  allocation_sequence INTEGER NOT NULL UNIQUE CHECK(allocation_sequence>0),
  generation BLOB NOT NULL CHECK(typeof(generation)='blob' AND length(generation)=8),
  tenant_id BLOB NOT NULL CHECK(typeof(tenant_id)='blob' AND length(tenant_id) BETWEEN 1 AND 256),
  principal_ref BLOB NOT NULL CHECK(typeof(principal_ref)='blob' AND length(principal_ref)=32),
  session_key_hash BLOB NOT NULL CHECK(typeof(session_key_hash)='blob' AND length(session_key_hash)=32),
  gateway_pid INTEGER NOT NULL CHECK(gateway_pid>0),
  gateway_uid INTEGER NOT NULL CHECK(gateway_uid>0),
  gateway_pidns_device BLOB NOT NULL CHECK(typeof(gateway_pidns_device)='blob' AND length(gateway_pidns_device)=8 AND gateway_pidns_device<>zeroblob(8)),
  gateway_pidns_inode BLOB NOT NULL CHECK(typeof(gateway_pidns_inode)='blob' AND length(gateway_pidns_inode)=8 AND gateway_pidns_inode<>zeroblob(8)),
  gateway_cgroup_mount_id BLOB NOT NULL CHECK(typeof(gateway_cgroup_mount_id)='blob' AND length(gateway_cgroup_mount_id)=8 AND gateway_cgroup_mount_id<>zeroblob(8)),
  gateway_cgroup_device BLOB NOT NULL CHECK(typeof(gateway_cgroup_device)='blob' AND length(gateway_cgroup_device)=8 AND gateway_cgroup_device<>zeroblob(8)),
  gateway_cgroup_inode BLOB NOT NULL CHECK(typeof(gateway_cgroup_inode)='blob' AND length(gateway_cgroup_inode)=8 AND gateway_cgroup_inode<>zeroblob(8)),
  gateway_start_ticks BLOB NOT NULL CHECK(typeof(gateway_start_ticks)='blob' AND length(gateway_start_ticks)=8 AND gateway_start_ticks<>zeroblob(8)),
  schema_digest BLOB NOT NULL CHECK(typeof(schema_digest)='blob' AND length(schema_digest)=32),
  payload_digest BLOB NOT NULL CHECK(typeof(payload_digest)='blob' AND length(payload_digest)=32),
  request_id BLOB NOT NULL CHECK(typeof(request_id)='blob' AND length(request_id)=16),
  business_operation_id BLOB NOT NULL CHECK(typeof(business_operation_id)='blob' AND length(business_operation_id)=16),
  operation_id BLOB NOT NULL UNIQUE CHECK(typeof(operation_id)='blob' AND length(operation_id)=16),
  allocation_state TEXT NOT NULL CHECK(allocation_state IN ('prepared','executing','completed','abandoned','indeterminate')),
  run_terminal_state TEXT NOT NULL CHECK(run_terminal_state IN ('none','active','abandoned','indeterminate')),
  terminal_evidence_digest BLOB CHECK(terminal_evidence_digest IS NULL OR (typeof(terminal_evidence_digest)='blob' AND length(terminal_evidence_digest)=32)),
  allocated_at INTEGER NOT NULL CHECK(allocated_at>=0),
  UNIQUE(run_epoch,generation),
  UNIQUE(tenant_id,principal_ref,business_operation_id),
  UNIQUE(run_epoch,operation_id,tenant_id,principal_ref,business_operation_id,request_id,payload_digest,generation),
  FOREIGN KEY(generation,run_epoch) REFERENCES pilot_run_lifecycle(generation,run_epoch) ON DELETE RESTRICT ON UPDATE RESTRICT,
  FOREIGN KEY(generation,run_epoch,gateway_pid,gateway_uid,gateway_start_ticks,gateway_pidns_device,gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode) REFERENCES pilot_bootstrap_handoffs(generation,run_epoch,gateway_pid,gateway_uid,gateway_start_ticks,gateway_pidns_device,gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE TABLE run_root_operations (
  operation_id BLOB NOT NULL PRIMARY KEY CHECK(typeof(operation_id)='blob' AND length(operation_id)=16),
  tenant_id BLOB NOT NULL CHECK(typeof(tenant_id)='blob' AND length(tenant_id) BETWEEN 1 AND 256),
  principal_ref BLOB NOT NULL CHECK(typeof(principal_ref)='blob' AND length(principal_ref)=32),
  business_operation_id BLOB NOT NULL CHECK(typeof(business_operation_id)='blob' AND length(business_operation_id)=16),
  operation_kind TEXT NOT NULL CHECK(operation_kind IN ('run_root_create','pilot_run_close','pilot_no_root_abort')),
  request_id BLOB NOT NULL CHECK(typeof(request_id)='blob' AND length(request_id)=16),
  run_epoch BLOB NOT NULL CHECK(typeof(run_epoch)='blob' AND length(run_epoch)=16),
  allocated_run_epoch BLOB CHECK(allocated_run_epoch IS NULL OR (typeof(allocated_run_epoch)='blob' AND length(allocated_run_epoch)=16)),
  generation BLOB NOT NULL CHECK(typeof(generation)='blob' AND length(generation)=8),
  payload_digest BLOB NOT NULL CHECK(typeof(payload_digest)='blob' AND length(payload_digest)=32),
  operation_state TEXT NOT NULL CHECK(operation_state IN ('prepared','executing','completed','abandoned','indeterminate')),
  owner_generation BLOB NOT NULL CHECK(typeof(owner_generation)='blob' AND length(owner_generation)=8),
  response_code TEXT CHECK(response_code IS NULL OR response_code IN ('created','exists','abandoned','indeterminate','closed_without_root')),
  canonical_response BLOB CHECK(canonical_response IS NULL OR (typeof(canonical_response)='blob' AND length(canonical_response) BETWEEN 1 AND 65536)),
  receipt_digest BLOB CHECK(receipt_digest IS NULL OR (typeof(receipt_digest)='blob' AND length(receipt_digest)=32)),
  dispatch_commit_digest BLOB CHECK(dispatch_commit_digest IS NULL OR (typeof(dispatch_commit_digest)='blob' AND length(dispatch_commit_digest)=32)),
  created_at INTEGER NOT NULL CHECK(created_at>=0),
  updated_at INTEGER NOT NULL CHECK(updated_at>=created_at),
  UNIQUE(tenant_id,principal_ref,business_operation_id),
  UNIQUE(operation_id,run_epoch,generation),
  UNIQUE(operation_id,run_epoch,principal_ref,payload_digest),
  CHECK((operation_kind='run_root_create' AND allocated_run_epoch IS NOT NULL AND allocated_run_epoch=run_epoch) OR (operation_kind<>'run_root_create' AND allocated_run_epoch IS NULL)),
  FOREIGN KEY(generation,run_epoch) REFERENCES pilot_run_lifecycle(generation,run_epoch) ON DELETE RESTRICT ON UPDATE RESTRICT,
  FOREIGN KEY(allocated_run_epoch,operation_id,tenant_id,principal_ref,business_operation_id,request_id,payload_digest,generation) REFERENCES run_epoch_allocations(run_epoch,operation_id,tenant_id,principal_ref,business_operation_id,request_id,payload_digest,generation) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE UNIQUE INDEX pilot_one_root_create_per_epoch ON run_root_operations(run_epoch) WHERE operation_kind='run_root_create';
CREATE UNIQUE INDEX pilot_one_close_per_epoch ON run_root_operations(run_epoch) WHERE operation_kind IN ('pilot_run_close','pilot_no_root_abort');

CREATE TABLE pilot_run_lease (
  singleton_id INTEGER NOT NULL PRIMARY KEY CHECK(singleton_id=1),
  run_epoch BLOB,
  generation BLOB,
  operation_id BLOB,
  updated_at INTEGER NOT NULL CHECK(updated_at>=0),
  CHECK((run_epoch IS NULL AND generation IS NULL AND operation_id IS NULL) OR (typeof(run_epoch)='blob' AND length(run_epoch)=16 AND typeof(generation)='blob' AND length(generation)=8 AND typeof(operation_id)='blob' AND length(operation_id)=16)),
  FOREIGN KEY(run_epoch,generation) REFERENCES run_epoch_allocations(run_epoch,generation) ON DELETE RESTRICT ON UPDATE RESTRICT,
  FOREIGN KEY(operation_id,run_epoch,generation) REFERENCES run_root_operations(operation_id,run_epoch,generation) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_business_capacity_counts (
  scope_kind TEXT NOT NULL CHECK(scope_kind IN ('global','tenant','principal')),
  tenant_id BLOB NOT NULL,
  principal_ref BLOB NOT NULL CHECK(typeof(principal_ref)='blob' AND length(principal_ref)=32),
  count INTEGER NOT NULL CHECK(count>=0),
  PRIMARY KEY(scope_kind,tenant_id,principal_ref),
  CHECK((scope_kind='global' AND tenant_id=X'' AND principal_ref=zeroblob(32)) OR
        (scope_kind='tenant' AND typeof(tenant_id)='blob' AND length(tenant_id) BETWEEN 1 AND 256 AND principal_ref=zeroblob(32)) OR
        (scope_kind='principal' AND typeof(tenant_id)='blob' AND length(tenant_id) BETWEEN 1 AND 256))
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_capacity_policy (
  singleton_id INTEGER NOT NULL PRIMARY KEY CHECK(singleton_id=1),
  global_limit INTEGER NOT NULL CHECK(global_limit>0),
  tenant_limit INTEGER NOT NULL CHECK(tenant_limit>0),
  principal_limit INTEGER NOT NULL CHECK(principal_limit>0 AND principal_limit<=tenant_limit AND tenant_limit<=global_limit)
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_capacity_alert_events (
  scope_kind TEXT NOT NULL CHECK(scope_kind IN ('global','tenant','principal')),
  scope_tenant_id BLOB NOT NULL,
  scope_principal_ref BLOB NOT NULL CHECK(typeof(scope_principal_ref)='blob' AND length(scope_principal_ref)=32),
  threshold_percent INTEGER NOT NULL CHECK(threshold_percent IN (80,90)),
  first_crossing_count INTEGER NOT NULL CHECK(first_crossing_count>0),
  crossing_run_epoch BLOB NOT NULL CHECK(typeof(crossing_run_epoch)='blob' AND length(crossing_run_epoch)=16),
  crossed_at INTEGER NOT NULL CHECK(crossed_at>=0),
  PRIMARY KEY(scope_kind,scope_tenant_id,scope_principal_ref,threshold_percent),
  CHECK((scope_kind='global' AND scope_tenant_id=X'' AND scope_principal_ref=zeroblob(32)) OR
        (scope_kind='tenant' AND typeof(scope_tenant_id)='blob' AND length(scope_tenant_id) BETWEEN 1 AND 256 AND scope_principal_ref=zeroblob(32)) OR
        (scope_kind='principal' AND typeof(scope_tenant_id)='blob' AND length(scope_tenant_id) BETWEEN 1 AND 256))
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_operation_evidence_receipts (
  receipt_digest BLOB NOT NULL PRIMARY KEY CHECK(typeof(receipt_digest)='blob' AND length(receipt_digest)=32),
  operation_id BLOB NOT NULL UNIQUE CHECK(typeof(operation_id)='blob' AND length(operation_id)=16),
  run_epoch BLOB NOT NULL CHECK(typeof(run_epoch)='blob' AND length(run_epoch)=16),
  principal_ref BLOB NOT NULL CHECK(typeof(principal_ref)='blob' AND length(principal_ref)=32),
  payload_digest BLOB NOT NULL CHECK(typeof(payload_digest)='blob' AND length(payload_digest)=32),
  canonical_receipt BLOB NOT NULL CHECK(typeof(canonical_receipt)='blob' AND length(canonical_receipt) BETWEEN 1 AND 65536),
  created_at INTEGER NOT NULL CHECK(created_at>=0),
  FOREIGN KEY(operation_id,run_epoch,principal_ref,payload_digest) REFERENCES run_root_operations(operation_id,run_epoch,principal_ref,payload_digest) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_prepared_cancel_receipts (
  receipt_digest BLOB NOT NULL PRIMARY KEY CHECK(typeof(receipt_digest)='blob' AND length(receipt_digest)=32),
  operation_id BLOB NOT NULL UNIQUE CHECK(typeof(operation_id)='blob' AND length(operation_id)=16),
  generation BLOB NOT NULL CHECK(typeof(generation)='blob' AND length(generation)=8),
  run_epoch BLOB NOT NULL CHECK(typeof(run_epoch)='blob' AND length(run_epoch)=16),
  close_operation_id BLOB NOT NULL CHECK(typeof(close_operation_id)='blob' AND length(close_operation_id)=16),
  intent_delivery_disposition TEXT NOT NULL CHECK(intent_delivery_disposition IN ('acknowledged','never_authorized')),
  intent_digest BLOB CHECK(intent_digest IS NULL OR (typeof(intent_digest)='blob' AND length(intent_digest)=32)),
  intent_correlation BLOB CHECK(intent_correlation IS NULL OR (typeof(intent_correlation)='blob' AND length(intent_correlation)=16)),
  absence_witness_digest BLOB NOT NULL CHECK(typeof(absence_witness_digest)='blob' AND length(absence_witness_digest)=32),
  canonical_receipt BLOB NOT NULL CHECK(typeof(canonical_receipt)='blob' AND length(canonical_receipt) BETWEEN 1 AND 8192),
  created_at INTEGER NOT NULL CHECK(created_at>=0),
  CHECK((intent_delivery_disposition='acknowledged' AND intent_digest IS NOT NULL AND intent_correlation IS NOT NULL) OR (intent_delivery_disposition='never_authorized' AND intent_digest IS NOT NULL AND intent_correlation IS NULL)),
  FOREIGN KEY(operation_id,run_epoch,generation) REFERENCES run_root_operations(operation_id,run_epoch,generation) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_run_containment_evidence (
  run_epoch BLOB NOT NULL CHECK(typeof(run_epoch)='blob' AND length(run_epoch)=16),
  gateway_generation BLOB NOT NULL CHECK(typeof(gateway_generation)='blob' AND length(gateway_generation)=8),
  launch_nonce TEXT NOT NULL CHECK(length(CAST(launch_nonce AS BLOB))=32 AND length(launch_nonce)=32 AND launch_nonce NOT GLOB '*[^0-9a-f]*'),
  gateway_pid INTEGER NOT NULL CHECK(gateway_pid>0),
  gateway_uid INTEGER NOT NULL CHECK(gateway_uid>0),
  gateway_start_ticks BLOB NOT NULL CHECK(typeof(gateway_start_ticks)='blob' AND length(gateway_start_ticks)=8 AND gateway_start_ticks<>zeroblob(8)),
  gateway_pidns_device BLOB NOT NULL CHECK(typeof(gateway_pidns_device)='blob' AND length(gateway_pidns_device)=8 AND gateway_pidns_device<>zeroblob(8)),
  gateway_pidns_inode BLOB NOT NULL CHECK(typeof(gateway_pidns_inode)='blob' AND length(gateway_pidns_inode)=8 AND gateway_pidns_inode<>zeroblob(8)),
  gateway_cgroup_mount_id BLOB NOT NULL CHECK(typeof(gateway_cgroup_mount_id)='blob' AND length(gateway_cgroup_mount_id)=8 AND gateway_cgroup_mount_id<>zeroblob(8)),
  gateway_cgroup_device BLOB NOT NULL CHECK(typeof(gateway_cgroup_device)='blob' AND length(gateway_cgroup_device)=8 AND gateway_cgroup_device<>zeroblob(8)),
  gateway_cgroup_inode BLOB NOT NULL CHECK(typeof(gateway_cgroup_inode)='blob' AND length(gateway_cgroup_inode)=8 AND gateway_cgroup_inode<>zeroblob(8)),
  endpoint_parent_device BLOB NOT NULL CHECK(typeof(endpoint_parent_device)='blob' AND length(endpoint_parent_device)=8 AND endpoint_parent_device<>zeroblob(8)),
  endpoint_parent_inode BLOB NOT NULL CHECK(typeof(endpoint_parent_inode)='blob' AND length(endpoint_parent_inode)=8 AND endpoint_parent_inode<>zeroblob(8)),
  endpoint_device BLOB NOT NULL CHECK(typeof(endpoint_device)='blob' AND length(endpoint_device)=8 AND endpoint_device<>zeroblob(8)),
  endpoint_inode BLOB NOT NULL CHECK(typeof(endpoint_inode)='blob' AND length(endpoint_inode)=8 AND endpoint_inode<>zeroblob(8)),
  endpoint_removed INTEGER NOT NULL CHECK(endpoint_removed=1),
  leader_reaped INTEGER NOT NULL CHECK(leader_reaped=1),
  populated INTEGER NOT NULL CHECK(populated=0),
  evidence_version INTEGER NOT NULL CHECK(evidence_version=1),
  launcher_evidence_digest BLOB NOT NULL CHECK(typeof(launcher_evidence_digest)='blob' AND length(launcher_evidence_digest)=32),
  evidence_digest BLOB NOT NULL CHECK(typeof(evidence_digest)='blob' AND length(evidence_digest)=32),
  canonical_receipt BLOB NOT NULL CHECK(typeof(canonical_receipt)='blob' AND length(canonical_receipt) BETWEEN 1 AND 65536),
  recorded_at INTEGER NOT NULL CHECK(recorded_at>=0),
  PRIMARY KEY(run_epoch,gateway_generation),
  UNIQUE(run_epoch,evidence_digest),
  FOREIGN KEY(gateway_generation,run_epoch,launch_nonce,gateway_pid,gateway_uid,gateway_start_ticks,gateway_pidns_device,gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode) REFERENCES pilot_bootstrap_handoffs(generation,run_epoch,launch_nonce,gateway_pid,gateway_uid,gateway_start_ticks,gateway_pidns_device,gateway_pidns_inode,gateway_cgroup_mount_id,gateway_cgroup_device,gateway_cgroup_inode) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_run_cleanup_dispositions (
  run_epoch BLOB NOT NULL PRIMARY KEY CHECK(typeof(run_epoch)='blob' AND length(run_epoch)=16),
  disposition TEXT NOT NULL CHECK(disposition IN ('complete','retained','indeterminate')),
  root_mount_id BLOB NOT NULL CHECK(typeof(root_mount_id)='blob' AND length(root_mount_id)=8 AND root_mount_id<>zeroblob(8)),
  root_device BLOB NOT NULL CHECK(typeof(root_device)='blob' AND length(root_device)=8 AND root_device<>zeroblob(8)),
  root_inode BLOB NOT NULL CHECK(typeof(root_inode)='blob' AND length(root_inode)=8 AND root_inode<>zeroblob(8)),
  disposition_digest BLOB NOT NULL CHECK(typeof(disposition_digest)='blob' AND length(disposition_digest)=32),
  canonical_evidence BLOB NOT NULL CHECK(typeof(canonical_evidence)='blob' AND length(canonical_evidence) BETWEEN 1 AND 8192),
  recorded_at INTEGER NOT NULL CHECK(recorded_at>=0),
  FOREIGN KEY(run_epoch) REFERENCES run_epoch_allocations(run_epoch) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE TABLE pilot_root_run_state (
  run_epoch BLOB NOT NULL PRIMARY KEY CHECK(typeof(run_epoch)='blob' AND length(run_epoch)=16),
  root_mount_id BLOB NOT NULL CHECK(typeof(root_mount_id)='blob' AND length(root_mount_id)=8 AND root_mount_id<>zeroblob(8)),
  root_device BLOB NOT NULL CHECK(typeof(root_device)='blob' AND length(root_device)=8 AND root_device<>zeroblob(8)),
  root_inode BLOB NOT NULL CHECK(typeof(root_inode)='blob' AND length(root_inode)=8 AND root_inode<>zeroblob(8)),
  run_state TEXT NOT NULL CHECK(run_state IN ('active','terminating','containment_empty','abandoned')),
  cleanup_state TEXT NOT NULL CHECK(cleanup_state IN ('not_required','pending','complete','retained','indeterminate')),
  containment_evidence_digest BLOB CHECK(containment_evidence_digest IS NULL OR (typeof(containment_evidence_digest)='blob' AND length(containment_evidence_digest)=32)),
  updated_at INTEGER NOT NULL CHECK(updated_at>=0),
  FOREIGN KEY(run_epoch) REFERENCES run_epoch_allocations(run_epoch) ON DELETE RESTRICT ON UPDATE RESTRICT,
  FOREIGN KEY(run_epoch,containment_evidence_digest) REFERENCES pilot_run_containment_evidence(run_epoch,evidence_digest) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

CREATE TABLE run_root_event_outbox (
  outbox_id BLOB NOT NULL PRIMARY KEY CHECK(typeof(outbox_id)='blob' AND length(outbox_id)=16),
  operation_id BLOB NOT NULL CHECK(typeof(operation_id)='blob' AND length(operation_id)=16),
  event_name TEXT NOT NULL CHECK(event_name IN ('run_root_dispatch_intent','bootstrap.finish')),
  process_id INTEGER NOT NULL CHECK(process_id>0),
  canonical_event BLOB NOT NULL CHECK(typeof(canonical_event)='blob' AND length(canonical_event) BETWEEN 1 AND 65536),
  event_digest BLOB NOT NULL CHECK(typeof(event_digest)='blob' AND length(event_digest)=32),
  delivery_state TEXT NOT NULL CHECK(delivery_state IN ('ready','send_authorized','acknowledged','suppressed')),
  acknowledged_digest BLOB CHECK(acknowledged_digest IS NULL OR (typeof(acknowledged_digest)='blob' AND length(acknowledged_digest)=32)),
  acknowledged_correlation BLOB CHECK(acknowledged_correlation IS NULL OR (typeof(acknowledged_correlation)='blob' AND length(acknowledged_correlation)=16)),
  suppression_receipt_digest BLOB CHECK(suppression_receipt_digest IS NULL OR (typeof(suppression_receipt_digest)='blob' AND length(suppression_receipt_digest)=32)),
  created_at INTEGER NOT NULL CHECK(created_at>=0),
  UNIQUE(operation_id,event_name),
  CHECK((delivery_state='acknowledged' AND acknowledged_digest IS NOT NULL AND acknowledged_correlation IS NOT NULL AND suppression_receipt_digest IS NULL) OR
        (delivery_state='suppressed' AND event_name='run_root_dispatch_intent' AND acknowledged_digest IS NULL AND acknowledged_correlation IS NULL AND suppression_receipt_digest IS NOT NULL) OR
        (delivery_state IN ('ready','send_authorized') AND acknowledged_digest IS NULL AND acknowledged_correlation IS NULL AND suppression_receipt_digest IS NULL)),
  FOREIGN KEY(operation_id) REFERENCES run_root_operations(operation_id) ON DELETE RESTRICT ON UPDATE RESTRICT,
  FOREIGN KEY(suppression_receipt_digest) REFERENCES pilot_prepared_cancel_receipts(receipt_digest) ON DELETE RESTRICT ON UPDATE RESTRICT
) STRICT, WITHOUT ROWID;

-- Sole allowed cross-table trigger. It creates missing exact scope rows,
-- validates quota before incrementing, and rolls all writes back on failure.
CREATE TRIGGER pilot_allocation_capacity_after_insert AFTER INSERT ON run_epoch_allocations
BEGIN
  SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM pilot_capacity_policy WHERE singleton_id=1)
    THEN RAISE(ABORT,'capacity_policy_missing') END;
  INSERT INTO pilot_business_capacity_counts(scope_kind,tenant_id,principal_ref,count)
    SELECT 'tenant',NEW.tenant_id,zeroblob(32),0
    WHERE NOT EXISTS(SELECT 1 FROM pilot_business_capacity_counts WHERE scope_kind='tenant' AND tenant_id=NEW.tenant_id AND principal_ref=zeroblob(32));
  INSERT INTO pilot_business_capacity_counts(scope_kind,tenant_id,principal_ref,count)
    SELECT 'principal',NEW.tenant_id,NEW.principal_ref,0
    WHERE NOT EXISTS(SELECT 1 FROM pilot_business_capacity_counts WHERE scope_kind='principal' AND tenant_id=NEW.tenant_id AND principal_ref=NEW.principal_ref);
  SELECT CASE WHEN NOT EXISTS(SELECT 1 FROM pilot_business_capacity_counts WHERE scope_kind='global' AND tenant_id=X'' AND principal_ref=zeroblob(32))
    OR NOT EXISTS(SELECT 1 FROM pilot_business_capacity_counts WHERE scope_kind='tenant' AND tenant_id=NEW.tenant_id AND principal_ref=zeroblob(32))
    OR NOT EXISTS(SELECT 1 FROM pilot_business_capacity_counts WHERE scope_kind='principal' AND tenant_id=NEW.tenant_id AND principal_ref=NEW.principal_ref)
    THEN RAISE(ABORT,'capacity_scope_missing') END;
  SELECT CASE WHEN NEW.allocation_sequence<>(SELECT count+1 FROM pilot_business_capacity_counts WHERE scope_kind='global' AND tenant_id=X'' AND principal_ref=zeroblob(32))
    THEN RAISE(ABORT,'allocation_sequence') END;
  SELECT CASE WHEN
    (SELECT count FROM pilot_business_capacity_counts WHERE scope_kind='global' AND tenant_id=X'' AND principal_ref=zeroblob(32)) >= (SELECT global_limit FROM pilot_capacity_policy WHERE singleton_id=1)
    OR (SELECT count FROM pilot_business_capacity_counts WHERE scope_kind='tenant' AND tenant_id=NEW.tenant_id AND principal_ref=zeroblob(32)) >= (SELECT tenant_limit FROM pilot_capacity_policy WHERE singleton_id=1)
    OR (SELECT count FROM pilot_business_capacity_counts WHERE scope_kind='principal' AND tenant_id=NEW.tenant_id AND principal_ref=NEW.principal_ref) >= (SELECT principal_limit FROM pilot_capacity_policy WHERE singleton_id=1)
    THEN RAISE(ABORT,'capacity_exceeded') END;
  UPDATE pilot_business_capacity_counts SET count=count+1
    WHERE (scope_kind='global' AND tenant_id=X'' AND principal_ref=zeroblob(32))
       OR (scope_kind='tenant' AND tenant_id=NEW.tenant_id AND principal_ref=zeroblob(32))
       OR (scope_kind='principal' AND tenant_id=NEW.tenant_id AND principal_ref=NEW.principal_ref);
  INSERT INTO pilot_capacity_alert_events(scope_kind,scope_tenant_id,scope_principal_ref,threshold_percent,first_crossing_count,crossing_run_epoch,crossed_at)
    SELECT c.scope_kind,c.tenant_id,c.principal_ref,80,c.count,NEW.run_epoch,NEW.allocated_at
    FROM pilot_business_capacity_counts c JOIN pilot_capacity_policy p ON p.singleton_id=1
    WHERE c.count=(CASE c.scope_kind WHEN 'global' THEN p.global_limit WHEN 'tenant' THEN p.tenant_limit ELSE p.principal_limit END*80+99)/100
      AND ((c.scope_kind='global' AND c.tenant_id=X'' AND c.principal_ref=zeroblob(32)) OR
           (c.scope_kind='tenant' AND c.tenant_id=NEW.tenant_id AND c.principal_ref=zeroblob(32)) OR
           (c.scope_kind='principal' AND c.tenant_id=NEW.tenant_id AND c.principal_ref=NEW.principal_ref));
  INSERT INTO pilot_capacity_alert_events(scope_kind,scope_tenant_id,scope_principal_ref,threshold_percent,first_crossing_count,crossing_run_epoch,crossed_at)
    SELECT c.scope_kind,c.tenant_id,c.principal_ref,90,c.count,NEW.run_epoch,NEW.allocated_at
    FROM pilot_business_capacity_counts c JOIN pilot_capacity_policy p ON p.singleton_id=1
    WHERE c.count=(CASE c.scope_kind WHEN 'global' THEN p.global_limit WHEN 'tenant' THEN p.tenant_limit ELSE p.principal_limit END*90+99)/100
      AND ((c.scope_kind='global' AND c.tenant_id=X'' AND c.principal_ref=zeroblob(32)) OR
           (c.scope_kind='tenant' AND c.tenant_id=NEW.tenant_id AND c.principal_ref=zeroblob(32)) OR
           (c.scope_kind='principal' AND c.tenant_id=NEW.tenant_id AND c.principal_ref=NEW.principal_ref));
END;

-- Immutable relation guards. REPLACE prevention requires BEFORE INSERT guards
-- for every permanent unique identity; these are intentionally manifest-listed.
CREATE TRIGGER run_root_ledger_meta_no_update BEFORE UPDATE ON run_root_ledger_meta BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER run_root_ledger_meta_no_delete BEFORE DELETE ON run_root_ledger_meta BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER run_root_ledger_meta_no_replace BEFORE INSERT ON run_root_ledger_meta WHEN EXISTS(SELECT 1 FROM run_root_ledger_meta WHERE singleton=NEW.singleton) BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_control_generations_no_update BEFORE UPDATE ON pilot_control_generations BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_control_generations_no_delete BEFORE DELETE ON pilot_control_generations BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_control_generations_no_replace BEFORE INSERT ON pilot_control_generations WHEN EXISTS(SELECT 1 FROM pilot_control_generations WHERE generation=NEW.generation OR launch_nonce=NEW.launch_nonce) BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_principal_aliases_no_update BEFORE UPDATE ON pilot_principal_aliases BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_principal_aliases_no_delete BEFORE DELETE ON pilot_principal_aliases BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_principal_aliases_no_replace BEFORE INSERT ON pilot_principal_aliases WHEN EXISTS(SELECT 1 FROM pilot_principal_aliases WHERE (tenant_id=NEW.tenant_id AND identity_key_version=NEW.identity_key_version AND identity_digest=NEW.identity_digest) OR (tenant_id=NEW.tenant_id AND principal_ref=NEW.principal_ref AND identity_key_version=NEW.identity_key_version)) BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_authz_receipts_no_update BEFORE UPDATE ON pilot_authz_update_receipts BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_authz_receipts_no_delete BEFORE DELETE ON pilot_authz_update_receipts BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_authz_receipts_no_replace BEFORE INSERT ON pilot_authz_update_receipts WHEN EXISTS(SELECT 1 FROM pilot_authz_update_receipts WHERE request_id=NEW.request_id OR (tenant_id=NEW.tenant_id AND principal_ref=NEW.principal_ref AND target_kind=NEW.target_kind AND target_key=NEW.target_key AND revision=NEW.revision)) BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_handoffs_no_update BEFORE UPDATE ON pilot_bootstrap_handoffs BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_handoffs_no_delete BEFORE DELETE ON pilot_bootstrap_handoffs BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_handoffs_no_replace BEFORE INSERT ON pilot_bootstrap_handoffs WHEN EXISTS(SELECT 1 FROM pilot_bootstrap_handoffs WHERE generation=NEW.generation OR run_epoch=NEW.run_epoch OR launch_nonce=NEW.launch_nonce) BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_operation_receipts_no_update BEFORE UPDATE ON pilot_operation_evidence_receipts BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_operation_receipts_no_delete BEFORE DELETE ON pilot_operation_evidence_receipts BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_operation_receipts_no_replace BEFORE INSERT ON pilot_operation_evidence_receipts WHEN EXISTS(SELECT 1 FROM pilot_operation_evidence_receipts WHERE receipt_digest=NEW.receipt_digest OR operation_id=NEW.operation_id) BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_cancel_receipts_no_update BEFORE UPDATE ON pilot_prepared_cancel_receipts BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_cancel_receipts_no_delete BEFORE DELETE ON pilot_prepared_cancel_receipts BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_cancel_receipts_no_replace BEFORE INSERT ON pilot_prepared_cancel_receipts WHEN EXISTS(SELECT 1 FROM pilot_prepared_cancel_receipts WHERE receipt_digest=NEW.receipt_digest OR operation_id=NEW.operation_id) BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_containment_evidence_no_update BEFORE UPDATE ON pilot_run_containment_evidence BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_containment_evidence_no_delete BEFORE DELETE ON pilot_run_containment_evidence BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_containment_evidence_no_replace BEFORE INSERT ON pilot_run_containment_evidence WHEN EXISTS(SELECT 1 FROM pilot_run_containment_evidence WHERE run_epoch=NEW.run_epoch AND gateway_generation=NEW.gateway_generation) BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_cleanup_dispositions_no_update BEFORE UPDATE ON pilot_run_cleanup_dispositions BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_cleanup_dispositions_no_delete BEFORE DELETE ON pilot_run_cleanup_dispositions BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_cleanup_dispositions_no_replace BEFORE INSERT ON pilot_run_cleanup_dispositions WHEN EXISTS(SELECT 1 FROM pilot_run_cleanup_dispositions WHERE run_epoch=NEW.run_epoch) BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_capacity_policy_no_update BEFORE UPDATE ON pilot_capacity_policy BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_capacity_policy_no_delete BEFORE DELETE ON pilot_capacity_policy BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_capacity_policy_no_replace BEFORE INSERT ON pilot_capacity_policy WHEN EXISTS(SELECT 1 FROM pilot_capacity_policy WHERE singleton_id=NEW.singleton_id) BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_capacity_counts_no_delete BEFORE DELETE ON pilot_business_capacity_counts BEGIN SELECT RAISE(ABORT,'capacity_count_immutable'); END;
CREATE TRIGGER pilot_capacity_alerts_no_update BEFORE UPDATE ON pilot_capacity_alert_events BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_capacity_alerts_no_delete BEFORE DELETE ON pilot_capacity_alert_events BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_capacity_alerts_no_replace BEFORE INSERT ON pilot_capacity_alert_events WHEN EXISTS(SELECT 1 FROM pilot_capacity_alert_events WHERE scope_kind=NEW.scope_kind AND scope_tenant_id=NEW.scope_tenant_id AND scope_principal_ref=NEW.scope_principal_ref AND threshold_percent=NEW.threshold_percent) BEGIN SELECT RAISE(ABORT,'immutable'); END;
CREATE TRIGGER pilot_lease_no_delete BEFORE DELETE ON pilot_run_lease BEGIN SELECT RAISE(ABORT,'lease_singleton'); END;
CREATE TRIGGER pilot_lease_no_replace BEFORE INSERT ON pilot_run_lease WHEN EXISTS(SELECT 1 FROM pilot_run_lease WHERE singleton_id=NEW.singleton_id) BEGIN SELECT RAISE(ABORT,'lease_singleton'); END;

-- Authorization rows are published only by signed monotonic fence updates.
CREATE TRIGGER pilot_authz_state_no_replace BEFORE INSERT ON pilot_authorization_state WHEN EXISTS(SELECT 1 FROM pilot_authorization_state WHERE tenant_id=NEW.tenant_id AND principal_ref=NEW.principal_ref) BEGIN SELECT RAISE(ABORT,'authz_exists'); END;
CREATE TRIGGER pilot_authz_state_no_delete BEFORE DELETE ON pilot_authorization_state BEGIN SELECT RAISE(ABORT,'authz_tombstone'); END;
CREATE TRIGGER pilot_authz_state_update_guard BEFORE UPDATE ON pilot_authorization_state
WHEN NEW.tenant_id<>OLD.tenant_id OR NEW.principal_ref<>OLD.principal_ref OR NEW.authz_revision<=OLD.authz_revision OR NEW.updated_at<OLD.updated_at
BEGIN SELECT RAISE(ABORT,'authz_revision'); END;
CREATE TRIGGER pilot_session_fence_no_replace BEFORE INSERT ON pilot_session_fences WHEN EXISTS(SELECT 1 FROM pilot_session_fences WHERE tenant_id=NEW.tenant_id AND principal_ref=NEW.principal_ref AND session_key_hash=NEW.session_key_hash) BEGIN SELECT RAISE(ABORT,'session_exists'); END;
CREATE TRIGGER pilot_session_fence_no_delete BEFORE DELETE ON pilot_session_fences BEGIN SELECT RAISE(ABORT,'session_tombstone'); END;
CREATE TRIGGER pilot_session_fence_update_guard BEFORE UPDATE ON pilot_session_fences
WHEN NEW.tenant_id<>OLD.tenant_id OR NEW.principal_ref<>OLD.principal_ref OR NEW.session_key_hash<>OLD.session_key_hash OR NEW.session_revision<=OLD.session_revision OR NEW.updated_at<OLD.updated_at OR NEW.expires_at>OLD.expires_at OR (OLD.session_state='revoked' AND NEW.session_state<>'revoked')
BEGIN SELECT RAISE(ABORT,'session_revision'); END;
CREATE TRIGGER pilot_fixture_grant_no_replace BEFORE INSERT ON pilot_fixture_grants WHEN EXISTS(SELECT 1 FROM pilot_fixture_grants WHERE tenant_id=NEW.tenant_id AND principal_ref=NEW.principal_ref AND fixture_id=NEW.fixture_id) BEGIN SELECT RAISE(ABORT,'grant_exists'); END;
CREATE TRIGGER pilot_fixture_grant_no_delete BEFORE DELETE ON pilot_fixture_grants BEGIN SELECT RAISE(ABORT,'grant_tombstone'); END;
CREATE TRIGGER pilot_fixture_grant_update_guard BEFORE UPDATE ON pilot_fixture_grants
WHEN NEW.tenant_id<>OLD.tenant_id OR NEW.principal_ref<>OLD.principal_ref OR NEW.fixture_id<>OLD.fixture_id OR NEW.grant_revision<=OLD.grant_revision OR NEW.updated_at<OLD.updated_at
BEGIN SELECT RAISE(ABORT,'grant_revision'); END;
-- Mutable host lifecycle permits only named monotonic edges and immutable bindings.
CREATE TRIGGER pilot_lifecycle_no_replace BEFORE INSERT ON pilot_run_lifecycle WHEN EXISTS(SELECT 1 FROM pilot_run_lifecycle WHERE generation=NEW.generation OR run_epoch=NEW.run_epoch) BEGIN SELECT RAISE(ABORT,'lifecycle_exists'); END;
CREATE TRIGGER pilot_lifecycle_insert_guard BEFORE INSERT ON pilot_run_lifecycle WHEN NEW.lifecycle_state<>'bootstrapped' OR NEW.run_root_operation_id IS NOT NULL OR NEW.close_operation_id IS NOT NULL OR NEW.terminal_result IS NOT NULL BEGIN SELECT RAISE(ABORT,'lifecycle_initial_state'); END;
CREATE TRIGGER pilot_lifecycle_no_delete BEFORE DELETE ON pilot_run_lifecycle BEGIN SELECT RAISE(ABORT,'lifecycle_tombstone'); END;
CREATE TRIGGER pilot_lifecycle_update_guard BEFORE UPDATE ON pilot_run_lifecycle
WHEN NEW.generation<>OLD.generation OR NEW.run_epoch<>OLD.run_epoch OR OLD.lifecycle_state='terminal' OR NEW.updated_at<OLD.updated_at
 OR NOT ((OLD.lifecycle_state=NEW.lifecycle_state) OR
   (OLD.lifecycle_state='bootstrapped' AND NEW.lifecycle_state='logger_registered') OR
   (OLD.lifecycle_state='logger_registered' AND NEW.lifecycle_state='logger_ready') OR
   (OLD.lifecycle_state='logger_ready' AND NEW.lifecycle_state='reaper_ready') OR
   (OLD.lifecycle_state='reaper_ready' AND NEW.lifecycle_state='registered') OR
   (OLD.lifecycle_state='registered' AND NEW.lifecycle_state IN ('active','terminating')) OR
   (OLD.lifecycle_state='active' AND NEW.lifecycle_state='terminating') OR
   (OLD.lifecycle_state='terminating' AND NEW.lifecycle_state='unregistered') OR
   (OLD.lifecycle_state='unregistered' AND NEW.lifecycle_state='containment_empty') OR
   (OLD.lifecycle_state='containment_empty' AND NEW.lifecycle_state='terminal'))
 OR (OLD.run_root_operation_id IS NOT NULL AND NEW.run_root_operation_id IS NOT OLD.run_root_operation_id)
 OR (OLD.run_root_operation_id IS NULL AND NEW.run_root_operation_id IS NOT NULL AND (NEW.lifecycle_state<>OLD.lifecycle_state OR OLD.lifecycle_state NOT IN ('registered','active')))
 OR (OLD.close_operation_id IS NULL AND NEW.close_operation_id IS NOT NULL AND (NEW.lifecycle_state<>'terminating' OR OLD.lifecycle_state NOT IN ('registered','active')))
 OR (OLD.close_operation_id IS NOT NULL AND (NEW.close_operation_id IS NOT OLD.close_operation_id OR NEW.close_key_hash IS NOT OLD.close_key_hash OR NEW.close_payload_digest IS NOT OLD.close_payload_digest))
BEGIN SELECT RAISE(ABORT,'lifecycle_transition'); END;

-- Permanent tombstones and operations retain identity and only advance through legal CAS edges.
CREATE TRIGGER pilot_allocation_no_replace BEFORE INSERT ON run_epoch_allocations WHEN EXISTS(SELECT 1 FROM run_epoch_allocations WHERE run_epoch=NEW.run_epoch OR operation_id=NEW.operation_id OR (tenant_id=NEW.tenant_id AND principal_ref=NEW.principal_ref AND business_operation_id=NEW.business_operation_id)) BEGIN SELECT RAISE(ABORT,'allocation_exists'); END;
CREATE TRIGGER pilot_allocation_insert_guard BEFORE INSERT ON run_epoch_allocations WHEN NEW.allocation_state<>'prepared' OR NEW.run_terminal_state<>'none' OR NEW.terminal_evidence_digest IS NOT NULL BEGIN SELECT RAISE(ABORT,'allocation_initial_state'); END;
CREATE TRIGGER pilot_allocation_no_delete BEFORE DELETE ON run_epoch_allocations BEGIN SELECT RAISE(ABORT,'allocation_tombstone'); END;
CREATE TRIGGER pilot_allocation_update_guard BEFORE UPDATE ON run_epoch_allocations
WHEN NEW.run_epoch<>OLD.run_epoch OR NEW.allocation_sequence<>OLD.allocation_sequence OR NEW.generation<>OLD.generation OR NEW.tenant_id<>OLD.tenant_id OR NEW.principal_ref<>OLD.principal_ref OR NEW.session_key_hash<>OLD.session_key_hash OR NEW.request_id<>OLD.request_id OR NEW.business_operation_id<>OLD.business_operation_id OR NEW.operation_id<>OLD.operation_id OR NEW.payload_digest<>OLD.payload_digest OR NEW.schema_digest<>OLD.schema_digest OR NEW.gateway_pid<>OLD.gateway_pid OR NEW.gateway_uid<>OLD.gateway_uid OR NEW.gateway_start_ticks<>OLD.gateway_start_ticks OR NEW.gateway_pidns_device<>OLD.gateway_pidns_device OR NEW.gateway_pidns_inode<>OLD.gateway_pidns_inode OR NEW.gateway_cgroup_mount_id<>OLD.gateway_cgroup_mount_id OR NEW.gateway_cgroup_device<>OLD.gateway_cgroup_device OR NEW.gateway_cgroup_inode<>OLD.gateway_cgroup_inode OR NEW.allocated_at<>OLD.allocated_at OR (OLD.terminal_evidence_digest IS NOT NULL AND NEW.terminal_evidence_digest IS NOT OLD.terminal_evidence_digest) OR (NEW.terminal_evidence_digest IS NOT NULL AND NEW.run_terminal_state NOT IN ('abandoned','indeterminate'))
 OR NOT ((OLD.allocation_state='prepared' AND NEW.allocation_state IN ('executing','completed','abandoned','indeterminate')) OR (OLD.allocation_state='executing' AND NEW.allocation_state IN ('completed','abandoned','indeterminate')) OR OLD.allocation_state=NEW.allocation_state)
 OR NOT ((OLD.run_terminal_state='none' AND NEW.run_terminal_state IN ('none','active','abandoned','indeterminate')) OR (OLD.run_terminal_state='active' AND NEW.run_terminal_state IN ('active','abandoned','indeterminate')) OR OLD.run_terminal_state=NEW.run_terminal_state)
 OR (OLD.run_terminal_state IN ('abandoned','indeterminate') AND NEW.run_terminal_state<>OLD.run_terminal_state)
BEGIN SELECT RAISE(ABORT,'allocation_transition'); END;
CREATE TRIGGER pilot_operation_no_replace BEFORE INSERT ON run_root_operations WHEN EXISTS(SELECT 1 FROM run_root_operations WHERE operation_id=NEW.operation_id OR (tenant_id=NEW.tenant_id AND principal_ref=NEW.principal_ref AND business_operation_id=NEW.business_operation_id) OR (NEW.operation_kind='run_root_create' AND operation_kind='run_root_create' AND run_epoch=NEW.run_epoch) OR (NEW.operation_kind IN ('pilot_run_close','pilot_no_root_abort') AND operation_kind IN ('pilot_run_close','pilot_no_root_abort') AND run_epoch=NEW.run_epoch)) BEGIN SELECT RAISE(ABORT,'operation_exists'); END;
CREATE TRIGGER pilot_operation_insert_guard BEFORE INSERT ON run_root_operations WHEN NEW.operation_state<>'prepared' OR NEW.owner_generation<>zeroblob(8) OR NEW.response_code IS NOT NULL OR NEW.canonical_response IS NOT NULL OR NEW.receipt_digest IS NOT NULL OR NEW.dispatch_commit_digest IS NOT NULL BEGIN SELECT RAISE(ABORT,'operation_initial_state'); END;
CREATE TRIGGER pilot_operation_no_delete BEFORE DELETE ON run_root_operations BEGIN SELECT RAISE(ABORT,'operation_tombstone'); END;
CREATE TRIGGER pilot_operation_update_guard BEFORE UPDATE ON run_root_operations
WHEN NEW.operation_id<>OLD.operation_id OR NEW.tenant_id<>OLD.tenant_id OR NEW.principal_ref<>OLD.principal_ref OR NEW.business_operation_id<>OLD.business_operation_id OR NEW.operation_kind<>OLD.operation_kind OR NEW.request_id<>OLD.request_id OR NEW.run_epoch<>OLD.run_epoch OR NEW.allocated_run_epoch IS NOT OLD.allocated_run_epoch OR NEW.generation<>OLD.generation OR NEW.payload_digest<>OLD.payload_digest OR NEW.created_at<>OLD.created_at OR NEW.updated_at<OLD.updated_at OR (OLD.owner_generation<>zeroblob(8) AND NEW.owner_generation<>OLD.owner_generation) OR (OLD.owner_generation=zeroblob(8) AND NEW.owner_generation<>zeroblob(8) AND NOT (OLD.operation_state='prepared' AND NEW.operation_state='executing')) OR (OLD.response_code IS NOT NULL AND NEW.response_code IS NOT OLD.response_code) OR (OLD.canonical_response IS NOT NULL AND NEW.canonical_response IS NOT OLD.canonical_response) OR (OLD.receipt_digest IS NOT NULL AND NEW.receipt_digest IS NOT OLD.receipt_digest) OR (OLD.dispatch_commit_digest IS NOT NULL AND NEW.dispatch_commit_digest IS NOT OLD.dispatch_commit_digest) OR (OLD.operation_state IN ('completed','abandoned','indeterminate') AND (NEW.operation_state<>OLD.operation_state OR NEW.response_code IS NOT OLD.response_code OR NEW.canonical_response IS NOT OLD.canonical_response OR NEW.receipt_digest IS NOT OLD.receipt_digest OR NEW.dispatch_commit_digest IS NOT OLD.dispatch_commit_digest OR NEW.owner_generation<>OLD.owner_generation))
 OR (NEW.operation_state='executing' AND NEW.owner_generation=zeroblob(8))
 OR NOT ((OLD.operation_state='prepared' AND NEW.operation_state IN ('executing','abandoned','indeterminate')) OR (OLD.operation_state='executing' AND NEW.operation_state IN ('completed','abandoned','indeterminate')) OR OLD.operation_state=NEW.operation_state)
BEGIN SELECT RAISE(ABORT,'operation_transition'); END;

-- Lease ownership cannot be stolen or moved directly between two occupied owners.
CREATE TRIGGER pilot_lease_update_guard BEFORE UPDATE ON pilot_run_lease
WHEN NEW.singleton_id<>OLD.singleton_id OR NEW.updated_at<OLD.updated_at
 OR NOT ((OLD.run_epoch IS NULL AND OLD.generation IS NULL AND OLD.operation_id IS NULL AND ((NEW.run_epoch IS NULL AND NEW.generation IS NULL AND NEW.operation_id IS NULL) OR (NEW.run_epoch IS NOT NULL AND NEW.generation IS NOT NULL AND NEW.operation_id IS NOT NULL)))
      OR (OLD.run_epoch IS NOT NULL AND OLD.generation IS NOT NULL AND OLD.operation_id IS NOT NULL AND ((NEW.run_epoch IS NULL AND NEW.generation IS NULL AND NEW.operation_id IS NULL) OR (NEW.run_epoch=OLD.run_epoch AND NEW.generation=OLD.generation AND NEW.operation_id=OLD.operation_id))))
BEGIN SELECT RAISE(ABORT,'lease_owner_transition'); END;

-- Counter rows can only be created at zero and advanced by one; no decrease or REPLACE.
CREATE TRIGGER pilot_capacity_counts_no_replace BEFORE INSERT ON pilot_business_capacity_counts WHEN EXISTS(SELECT 1 FROM pilot_business_capacity_counts WHERE scope_kind=NEW.scope_kind AND tenant_id=NEW.tenant_id AND principal_ref=NEW.principal_ref) OR NEW.count<>0 BEGIN SELECT RAISE(ABORT,'capacity_count_insert'); END;
CREATE TRIGGER pilot_capacity_counts_update_guard BEFORE UPDATE ON pilot_business_capacity_counts
WHEN NEW.scope_kind<>OLD.scope_kind OR NEW.tenant_id<>OLD.tenant_id OR NEW.principal_ref<>OLD.principal_ref OR NEW.count<>OLD.count+1
BEGIN SELECT RAISE(ABORT,'capacity_count_increment'); END;

-- Root and outbox bindings remain fixed; state advances only along reviewed edges.
CREATE TRIGGER pilot_root_state_no_replace BEFORE INSERT ON pilot_root_run_state WHEN EXISTS(SELECT 1 FROM pilot_root_run_state WHERE run_epoch=NEW.run_epoch) BEGIN SELECT RAISE(ABORT,'root_state_exists'); END;
CREATE TRIGGER pilot_root_state_insert_guard BEFORE INSERT ON pilot_root_run_state WHEN NEW.run_state<>'active' OR NEW.cleanup_state<>'not_required' OR NEW.containment_evidence_digest IS NOT NULL BEGIN SELECT RAISE(ABORT,'root_state_initial'); END;
CREATE TRIGGER pilot_root_state_no_delete BEFORE DELETE ON pilot_root_run_state BEGIN SELECT RAISE(ABORT,'root_state_tombstone'); END;
CREATE TRIGGER pilot_root_state_update_guard BEFORE UPDATE ON pilot_root_run_state
WHEN NEW.run_epoch<>OLD.run_epoch OR NEW.root_mount_id<>OLD.root_mount_id OR NEW.root_device<>OLD.root_device OR NEW.root_inode<>OLD.root_inode OR NEW.updated_at<OLD.updated_at
 OR NOT ((OLD.run_state='active' AND NEW.run_state IN ('active','terminating')) OR (OLD.run_state='terminating' AND NEW.run_state IN ('terminating','containment_empty')) OR (OLD.run_state='containment_empty' AND NEW.run_state IN ('containment_empty','abandoned')) OR OLD.run_state=NEW.run_state)
 OR NOT ((OLD.cleanup_state='not_required' AND NEW.cleanup_state IN ('not_required','pending')) OR (OLD.cleanup_state='pending' AND NEW.cleanup_state IN ('pending','complete','retained','indeterminate')) OR OLD.cleanup_state=NEW.cleanup_state)
 OR (OLD.run_state='abandoned' AND NEW.run_state<>'abandoned') OR (OLD.cleanup_state IN ('complete','retained','indeterminate') AND NEW.cleanup_state<>OLD.cleanup_state)
 OR (OLD.containment_evidence_digest IS NOT NULL AND NEW.containment_evidence_digest IS NOT OLD.containment_evidence_digest) OR (NEW.containment_evidence_digest IS NOT NULL AND NEW.run_state NOT IN ('containment_empty','abandoned'))
BEGIN SELECT RAISE(ABORT,'root_state_transition'); END;
CREATE TRIGGER pilot_outbox_no_replace BEFORE INSERT ON run_root_event_outbox WHEN EXISTS(SELECT 1 FROM run_root_event_outbox WHERE outbox_id=NEW.outbox_id OR (operation_id=NEW.operation_id AND event_name=NEW.event_name)) BEGIN SELECT RAISE(ABORT,'outbox_exists'); END;
CREATE TRIGGER pilot_outbox_insert_guard BEFORE INSERT ON run_root_event_outbox WHEN NEW.delivery_state<>'ready' BEGIN SELECT RAISE(ABORT,'outbox_initial_state'); END;
CREATE TRIGGER pilot_outbox_no_delete BEFORE DELETE ON run_root_event_outbox BEGIN SELECT RAISE(ABORT,'outbox_tombstone'); END;
CREATE TRIGGER pilot_outbox_update_guard BEFORE UPDATE ON run_root_event_outbox
WHEN NEW.outbox_id<>OLD.outbox_id OR NEW.operation_id<>OLD.operation_id OR NEW.event_name<>OLD.event_name OR NEW.process_id<>OLD.process_id OR NEW.canonical_event<>OLD.canonical_event OR NEW.event_digest<>OLD.event_digest OR NEW.created_at<>OLD.created_at
 OR NOT ((OLD.delivery_state='ready' AND NEW.delivery_state IN ('ready','send_authorized','suppressed')) OR (OLD.delivery_state='send_authorized' AND NEW.delivery_state IN ('send_authorized','acknowledged')) OR (OLD.delivery_state IN ('acknowledged','suppressed') AND NEW.delivery_state=OLD.delivery_state))
 OR (OLD.delivery_state='acknowledged' AND (NEW.acknowledged_digest IS NOT OLD.acknowledged_digest OR NEW.acknowledged_correlation IS NOT OLD.acknowledged_correlation))
 OR (OLD.delivery_state='suppressed' AND NEW.suppression_receipt_digest IS NOT OLD.suppression_receipt_digest) OR (NEW.delivery_state='acknowledged' AND (NEW.acknowledged_digest<>NEW.event_digest OR NEW.acknowledged_correlation IS NULL))
BEGIN SELECT RAISE(ABORT,'outbox_transition'); END;
