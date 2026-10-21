-- Each query returns exactly one row with violation_count; acceptance requires 0.

-- INV-01 control generation and handoff are the same normal tuple
SELECT count(*) AS violation_count FROM pilot_bootstrap_handoffs h
LEFT JOIN pilot_control_generations c ON c.generation=h.generation AND c.launch_nonce=h.launch_nonce
WHERE c.generation IS NULL OR c.mode<>'normal' OR c.normal_run_epoch<>h.run_epoch OR h.control_mode<>c.mode;

-- INV-02 every lifecycle has exactly its immutable handoff tuple
SELECT count(*) AS violation_count FROM pilot_run_lifecycle l
LEFT JOIN pilot_bootstrap_handoffs h ON h.generation=l.generation AND h.run_epoch=l.run_epoch
WHERE h.generation IS NULL;

-- INV-03 operation, allocation, lifecycle and business identity bindings agree
WITH violations AS (
SELECT o.operation_id AS id FROM run_root_operations o
LEFT JOIN pilot_run_lifecycle l ON l.generation=o.generation AND l.run_epoch=o.run_epoch
LEFT JOIN run_epoch_allocations a ON a.run_epoch=o.allocated_run_epoch
WHERE l.generation IS NULL
 OR (o.operation_kind='run_root_create' AND l.run_root_operation_id IS NOT o.operation_id)
 OR (o.operation_kind IN ('pilot_run_close','pilot_no_root_abort') AND l.close_operation_id IS NOT o.operation_id)
 OR (o.operation_kind='run_root_create' AND
   (a.run_epoch IS NULL OR a.operation_id<>o.operation_id OR a.generation<>o.generation
    OR a.tenant_id<>o.tenant_id OR a.principal_ref<>o.principal_ref
    OR a.business_operation_id<>o.business_operation_id OR a.request_id<>o.request_id
    OR a.payload_digest<>o.payload_digest OR a.run_epoch<>o.run_epoch))
 OR (o.operation_kind<>'run_root_create' AND o.allocated_run_epoch IS NOT NULL)
 OR ((o.operation_kind='pilot_run_close' AND l.lifecycle_state='terminal' AND o.operation_state<>'completed')
  OR (o.operation_kind='pilot_no_root_abort' AND o.operation_state='completed' AND l.terminal_result<>'closed_without_root'))
UNION ALL
SELECT l.run_epoch FROM pilot_run_lifecycle l
LEFT JOIN run_root_operations o ON o.operation_id=l.run_root_operation_id AND o.generation=l.generation AND o.run_epoch=l.run_epoch
WHERE l.run_root_operation_id IS NOT NULL AND (o.operation_id IS NULL OR o.operation_kind<>'run_root_create')
UNION ALL
SELECT l.run_epoch FROM pilot_run_lifecycle l
LEFT JOIN run_root_operations o ON o.operation_id=l.close_operation_id AND o.generation=l.generation AND o.run_epoch=l.run_epoch
WHERE l.close_operation_id IS NOT NULL AND (o.operation_id IS NULL OR o.operation_kind NOT IN ('pilot_run_close','pilot_no_root_abort')
 OR o.payload_digest<>l.close_payload_digest)
)
SELECT count(*) AS violation_count FROM violations;

-- INV-04 exactly one lease singleton; occupied lease names its exact allocation and operation
SELECT CASE WHEN (SELECT count(*) FROM pilot_run_lease)<>1 THEN 1
 WHEN EXISTS(SELECT 1 FROM pilot_run_lease x WHERE x.singleton_id<>1) THEN 1
 WHEN EXISTS(SELECT 1 FROM pilot_run_lease x WHERE x.run_epoch IS NOT NULL AND NOT EXISTS(
   SELECT 1 FROM run_epoch_allocations a JOIN run_root_operations o
   ON o.operation_id=x.operation_id AND o.run_epoch=x.run_epoch AND o.generation=x.generation
   WHERE a.run_epoch=x.run_epoch AND a.generation=x.generation AND a.operation_id=x.operation_id)) THEN 1
 WHEN EXISTS(SELECT 1 FROM run_epoch_allocations a WHERE a.run_terminal_state<>'abandoned' AND NOT EXISTS(
   SELECT 1 FROM pilot_run_lease x WHERE x.run_epoch=a.run_epoch AND x.generation=a.generation AND x.operation_id=a.operation_id)) THEN 1
 WHEN EXISTS(SELECT 1 FROM run_epoch_allocations a JOIN pilot_run_lease x ON x.run_epoch=a.run_epoch WHERE a.run_terminal_state='abandoned') THEN 1
 ELSE 0 END AS violation_count;

-- INV-05 every created root-create has its exact receipt, finish event, and root-state; no other root-state exists
WITH expected(run_epoch,operation_id) AS (
 SELECT a.run_epoch,o.operation_id FROM run_epoch_allocations a JOIN run_root_operations o
 ON o.operation_id=a.operation_id AND o.run_epoch=a.run_epoch
 WHERE o.operation_kind='run_root_create' AND o.operation_state='completed' AND o.response_code='created'
), mismatch(run_epoch) AS (
 SELECT e.run_epoch FROM expected e LEFT JOIN pilot_root_run_state r USING(run_epoch) WHERE r.run_epoch IS NULL
 UNION ALL
 SELECT r.run_epoch FROM pilot_root_run_state r LEFT JOIN expected e USING(run_epoch) WHERE e.run_epoch IS NULL
 UNION ALL
 SELECT o.run_epoch FROM run_root_operations o WHERE o.operation_kind='run_root_create'
  AND o.operation_state='completed' AND o.response_code='created'
  AND (NOT EXISTS(SELECT 1 FROM pilot_operation_evidence_receipts x WHERE x.operation_id=o.operation_id
       AND x.receipt_digest=o.receipt_digest AND x.run_epoch=o.run_epoch
       AND x.principal_ref=o.principal_ref AND x.payload_digest=o.payload_digest)
   OR NOT EXISTS(SELECT 1 FROM run_root_event_outbox x WHERE x.operation_id=o.operation_id AND x.event_name='bootstrap.finish'))
)
SELECT count(*) AS violation_count FROM mismatch;

-- INV-06 acknowledged finish events have the matching operation receipt and all ACKs bind event bytes
SELECT count(*) AS violation_count FROM run_root_event_outbox e
LEFT JOIN run_root_operations o ON o.operation_id=e.operation_id
LEFT JOIN pilot_operation_evidence_receipts r ON r.operation_id=e.operation_id AND r.run_epoch=o.run_epoch
 AND r.principal_ref=o.principal_ref AND r.payload_digest=o.payload_digest
LEFT JOIN run_epoch_allocations a ON a.run_epoch=o.allocated_run_epoch AND a.operation_id=o.operation_id
LEFT JOIN pilot_run_lifecycle l ON l.generation=o.generation AND l.run_epoch=o.run_epoch
LEFT JOIN pilot_run_lease x ON x.singleton_id=1
LEFT JOIN pilot_root_run_state rootstate ON rootstate.run_epoch=o.run_epoch
  LEFT JOIN pilot_run_cleanup_dispositions cleanup ON cleanup.run_epoch=o.run_epoch
  LEFT JOIN pilot_run_containment_evidence containment ON containment.run_epoch=o.run_epoch
   AND containment.gateway_generation=o.generation AND containment.evidence_digest=rootstate.containment_evidence_digest
WHERE e.delivery_state='acknowledged' AND
 (e.acknowledged_digest<>e.event_digest OR e.acknowledged_correlation IS NULL OR o.operation_id IS NULL
  OR (e.event_name='bootstrap.finish' AND
    (r.operation_id IS NULL OR r.receipt_digest<>o.receipt_digest OR
       NOT ((o.response_code='created' AND o.operation_state='completed' AND a.allocation_state='completed'
              AND rootstate.run_epoch IS NOT NULL AND
              ((a.run_terminal_state='active' AND rootstate.run_state='active' AND rootstate.cleanup_state='not_required'
                AND x.run_epoch=o.run_epoch AND x.generation=o.generation AND x.operation_id=o.operation_id)
               OR (a.run_terminal_state='active' AND rootstate.run_state IN ('terminating','containment_empty')
                AND rootstate.cleanup_state IN ('pending','complete','retained')
                AND l.lifecycle_state IN ('terminating','unregistered','containment_empty')
                AND x.run_epoch=o.run_epoch AND x.generation=o.generation AND x.operation_id=o.operation_id)
               OR (a.run_terminal_state='abandoned' AND rootstate.run_state='abandoned'
                AND rootstate.cleanup_state IN ('complete','retained') AND cleanup.run_epoch IS NOT NULL
                AND cleanup.disposition IN ('complete','retained') AND containment.run_epoch IS NOT NULL
                AND NOT EXISTS(SELECT 1 FROM pilot_run_lease held WHERE held.run_epoch=o.run_epoch))))
       OR (o.response_code='exists' AND o.operation_state='abandoned' AND a.allocation_state='indeterminate'
            AND x.run_epoch IS NOT NULL
            AND a.run_terminal_state='indeterminate' AND x.run_epoch=o.run_epoch AND x.generation=o.generation
            AND x.operation_id=o.operation_id AND rootstate.run_epoch IS NULL
            AND l.lifecycle_state IN ('registered','terminating','unregistered','containment_empty'))))));

-- INV-07 terminal roots have exact containment/cleanup evidence, complete cleanup, terminal lifecycle and no lease
WITH violations(run_epoch) AS (
SELECT r.run_epoch FROM pilot_root_run_state r
JOIN pilot_run_lifecycle l ON l.run_epoch=r.run_epoch
JOIN run_epoch_allocations a ON a.run_epoch=r.run_epoch
LEFT JOIN run_root_operations rootop ON rootop.operation_id=l.run_root_operation_id AND rootop.run_epoch=l.run_epoch AND rootop.generation=l.generation
LEFT JOIN run_root_operations closeop ON closeop.operation_id=l.close_operation_id AND closeop.run_epoch=l.run_epoch AND closeop.generation=l.generation
LEFT JOIN pilot_operation_evidence_receipts close_receipt ON close_receipt.operation_id=closeop.operation_id
 AND close_receipt.run_epoch=closeop.run_epoch AND close_receipt.principal_ref=closeop.principal_ref
 AND close_receipt.payload_digest=closeop.payload_digest AND close_receipt.receipt_digest=closeop.receipt_digest
LEFT JOIN pilot_run_containment_evidence e ON e.run_epoch=r.run_epoch AND e.gateway_generation=l.generation AND e.evidence_digest=r.containment_evidence_digest
LEFT JOIN pilot_run_cleanup_dispositions d ON d.run_epoch=r.run_epoch
WHERE r.run_state='abandoned' AND (l.lifecycle_state<>'terminal' OR l.terminal_result<>'abandoned'
 OR a.allocation_state<>'completed' OR a.run_terminal_state<>'abandoned'
 OR rootop.operation_kind<>'run_root_create' OR rootop.operation_state<>'completed' OR rootop.response_code<>'created'
 OR closeop.operation_kind<>'pilot_run_close' OR closeop.operation_state<>'completed' OR closeop.response_code<>'abandoned'
 OR close_receipt.operation_id IS NULL OR e.run_epoch IS NULL
 OR d.run_epoch IS NULL OR d.disposition NOT IN ('complete','retained')
 OR r.cleanup_state NOT IN ('complete','retained')
 OR d.root_mount_id<>r.root_mount_id OR d.root_device<>r.root_device OR d.root_inode<>r.root_inode
 OR EXISTS(SELECT 1 FROM pilot_run_lease x WHERE x.run_epoch=r.run_epoch)
 OR EXISTS(SELECT 1 FROM run_root_event_outbox x WHERE x.operation_id IN
   (SELECT operation_id FROM run_root_operations WHERE run_epoch=r.run_epoch)
   AND x.delivery_state<>'acknowledged'))
UNION ALL
SELECT l.run_epoch FROM pilot_run_lifecycle l
LEFT JOIN pilot_root_run_state r ON r.run_epoch=l.run_epoch
LEFT JOIN run_epoch_allocations a ON a.run_epoch=l.run_epoch
LEFT JOIN run_root_operations rootop ON rootop.operation_id=l.run_root_operation_id AND rootop.run_epoch=l.run_epoch AND rootop.generation=l.generation
LEFT JOIN run_root_operations closeop ON closeop.operation_id=l.close_operation_id AND closeop.run_epoch=l.run_epoch AND closeop.generation=l.generation
LEFT JOIN pilot_operation_evidence_receipts close_receipt ON close_receipt.operation_id=closeop.operation_id
 AND close_receipt.run_epoch=closeop.run_epoch AND close_receipt.principal_ref=closeop.principal_ref
 AND close_receipt.payload_digest=closeop.payload_digest AND close_receipt.receipt_digest=closeop.receipt_digest
WHERE l.lifecycle_state='terminal' AND l.terminal_result='abandoned' AND l.run_root_operation_id IS NOT NULL
 AND NOT (
   (r.run_epoch IS NOT NULL AND r.run_state='abandoned' AND r.cleanup_state IN ('complete','retained')
    AND a.allocation_state='completed' AND a.run_terminal_state='abandoned'
    AND rootop.operation_kind='run_root_create' AND rootop.operation_state='completed' AND rootop.response_code='created'
    AND closeop.operation_kind='pilot_run_close' AND closeop.operation_state='completed' AND closeop.response_code='abandoned'
    AND close_receipt.operation_id IS NOT NULL)
   OR
   (r.run_epoch IS NULL AND a.allocation_state='abandoned' AND a.run_terminal_state='abandoned'
    AND rootop.operation_kind='run_root_create' AND rootop.operation_state='abandoned' AND rootop.response_code='abandoned'
    AND closeop.operation_kind='pilot_run_close' AND closeop.operation_state='completed' AND closeop.response_code='abandoned'
    AND close_receipt.operation_id IS NOT NULL
    AND EXISTS(SELECT 1 FROM pilot_prepared_cancel_receipts c WHERE c.operation_id=rootop.operation_id
      AND c.generation=l.generation AND c.run_epoch=l.run_epoch AND c.close_operation_id=closeop.operation_id)
    AND EXISTS(SELECT 1 FROM pilot_run_containment_evidence e WHERE e.run_epoch=l.run_epoch AND e.gateway_generation=l.generation)
    AND NOT EXISTS(SELECT 1 FROM pilot_run_lease x WHERE x.run_epoch=l.run_epoch)
    AND NOT EXISTS(SELECT 1 FROM run_root_event_outbox e JOIN run_root_operations x ON x.operation_id=e.operation_id
      WHERE x.run_epoch=l.run_epoch AND e.delivery_state NOT IN ('acknowledged','suppressed'))))
UNION ALL
SELECT l.run_epoch FROM pilot_run_lifecycle l
WHERE l.lifecycle_state='terminal' AND l.terminal_result='abandoned' AND l.run_root_operation_id IS NULL
UNION ALL
SELECT l.run_epoch FROM pilot_run_lifecycle l
LEFT JOIN run_root_operations o ON o.operation_id=l.close_operation_id AND o.run_epoch=l.run_epoch AND o.generation=l.generation
WHERE l.lifecycle_state='terminal' AND l.terminal_result='closed_without_root'
 AND (l.run_root_operation_id IS NOT NULL OR EXISTS(SELECT 1 FROM run_epoch_allocations a WHERE a.run_epoch=l.run_epoch)
  OR EXISTS(SELECT 1 FROM pilot_root_run_state r WHERE r.run_epoch=l.run_epoch)
  OR o.operation_id IS NULL OR o.operation_kind<>'pilot_no_root_abort' OR o.operation_state<>'completed'
  OR o.response_code<>'closed_without_root' OR EXISTS(SELECT 1 FROM pilot_run_lease x WHERE x.run_epoch=l.run_epoch)
  OR NOT EXISTS(SELECT 1 FROM pilot_operation_evidence_receipts cr WHERE cr.operation_id=o.operation_id
    AND cr.run_epoch=o.run_epoch AND cr.principal_ref=o.principal_ref AND cr.payload_digest=o.payload_digest
    AND cr.receipt_digest=o.receipt_digest)
  OR NOT EXISTS(SELECT 1 FROM pilot_run_containment_evidence e WHERE e.run_epoch=l.run_epoch AND e.gateway_generation=l.generation)
  OR EXISTS(SELECT 1 FROM run_root_event_outbox e JOIN run_root_operations x ON x.operation_id=e.operation_id
   WHERE x.run_epoch=l.run_epoch))
)
SELECT count(*) AS violation_count FROM violations;

-- INV-08 suppression is bound to this dispatch intent, create operation, close operation and epoch
SELECT count(*) AS violation_count FROM run_root_event_outbox e
JOIN run_root_operations o ON o.operation_id=e.operation_id
LEFT JOIN pilot_prepared_cancel_receipts c ON c.receipt_digest=e.suppression_receipt_digest
LEFT JOIN pilot_run_lifecycle l ON l.generation=o.generation AND l.run_epoch=o.run_epoch
LEFT JOIN run_root_operations closeop ON closeop.operation_id=c.close_operation_id
WHERE e.delivery_state='suppressed' AND (e.event_name<>'run_root_dispatch_intent' OR c.receipt_digest IS NULL
 OR c.operation_id<>o.operation_id OR c.run_epoch<>o.run_epoch OR c.generation<>o.generation
 OR c.intent_digest<>e.event_digest OR c.intent_delivery_disposition<>'never_authorized'
 OR l.close_operation_id<>c.close_operation_id OR closeop.operation_kind<>'pilot_run_close'
 OR closeop.run_epoch<>o.run_epoch OR closeop.generation<>o.generation
 OR closeop.tenant_id<>o.tenant_id OR closeop.principal_ref<>o.principal_ref
 OR closeop.payload_digest<>l.close_payload_digest);

-- INV-09 account/session authorization values each have one exact latest signed receipt
WITH problems(n) AS (
 SELECT count(*) FROM pilot_authorization_state a
LEFT JOIN pilot_authz_update_receipts r ON r.tenant_id=a.tenant_id AND r.principal_ref=a.principal_ref
 AND r.target_kind='account' AND r.target_key=zeroblob(32) AND r.revision=a.authz_revision
WHERE r.request_id IS NULL
 UNION ALL
 SELECT count(*) FROM pilot_session_fences s
LEFT JOIN pilot_authz_update_receipts r ON r.tenant_id=s.tenant_id AND r.principal_ref=s.principal_ref
 AND r.target_kind='session' AND r.target_key=s.session_key_hash AND r.revision=s.session_revision
WHERE r.request_id IS NULL
 UNION ALL
 SELECT count(*) FROM pilot_fixture_grants g
LEFT JOIN pilot_authorization_state a ON a.tenant_id=g.tenant_id AND a.principal_ref=g.principal_ref
LEFT JOIN pilot_authz_update_receipts r ON r.tenant_id=g.tenant_id AND r.principal_ref=g.principal_ref
 AND r.target_kind='account' AND r.target_key=zeroblob(32) AND r.revision=g.grant_revision
 WHERE a.principal_ref IS NULL OR r.request_id IS NULL OR g.grant_revision<>a.authz_revision
)
SELECT coalesce(sum(n),0) AS violation_count FROM problems;

-- INV-10 exact global/tenant/principal scope inventory and counts match permanent allocation tombstones
WITH expected(scope_kind,tenant_id,principal_ref,n) AS (
 SELECT 'global',X'',zeroblob(32),count(*) FROM run_epoch_allocations
 UNION ALL SELECT 'tenant',tenant_id,zeroblob(32),count(*) FROM run_epoch_allocations GROUP BY tenant_id
 UNION ALL SELECT 'principal',tenant_id,principal_ref,count(*) FROM run_epoch_allocations GROUP BY tenant_id,principal_ref
), mismatch(scope_kind,tenant_id,principal_ref) AS (
 SELECT e.scope_kind,e.tenant_id,e.principal_ref FROM expected e LEFT JOIN pilot_business_capacity_counts c USING(scope_kind,tenant_id,principal_ref)
 WHERE c.scope_kind IS NULL OR c.count<>e.n
 UNION ALL
 SELECT c.scope_kind,c.tenant_id,c.principal_ref FROM pilot_business_capacity_counts c LEFT JOIN expected e USING(scope_kind,tenant_id,principal_ref)
 WHERE e.scope_kind IS NULL
), sequence_gap(n) AS (
 SELECT CASE WHEN count(*)=0 THEN 0 WHEN min(allocation_sequence)=1 AND max(allocation_sequence)=count(*) THEN 0 ELSE 1 END
 FROM run_epoch_allocations
)
SELECT (SELECT count(*) FROM mismatch)+(SELECT n FROM sequence_gap) AS violation_count;

-- INV-11 crossing alert inventory and threshold counts match immutable policy and current exact counters
WITH scoped_allocations(scope_kind,tenant_id,principal_ref,run_epoch,allocated_at,ordinal) AS (
 SELECT 'global',X'',zeroblob(32),run_epoch,allocated_at,row_number() OVER(ORDER BY allocation_sequence)
 FROM run_epoch_allocations
 UNION ALL
 SELECT 'tenant',tenant_id,zeroblob(32),run_epoch,allocated_at,row_number() OVER(PARTITION BY tenant_id ORDER BY allocation_sequence)
 FROM run_epoch_allocations
 UNION ALL
 SELECT 'principal',tenant_id,principal_ref,run_epoch,allocated_at,row_number() OVER(PARTITION BY tenant_id,principal_ref ORDER BY allocation_sequence)
 FROM run_epoch_allocations
), thresholds(scope_kind,tenant_id,principal_ref,current_count,threshold_percent,target_count) AS (
 SELECT c.scope_kind,c.tenant_id,c.principal_ref,c.count,t.threshold,
  (CASE c.scope_kind WHEN 'global' THEN p.global_limit WHEN 'tenant' THEN p.tenant_limit ELSE p.principal_limit END*t.threshold+99)/100
 FROM pilot_business_capacity_counts c CROSS JOIN pilot_capacity_policy p
 CROSS JOIN (SELECT 80 AS threshold UNION ALL SELECT 90) t
), expected(scope_kind,tenant_id,principal_ref,threshold_percent,first_crossing_count,crossing_run_epoch,crossed_at) AS (
 SELECT t.scope_kind,t.tenant_id,t.principal_ref,t.threshold_percent,t.target_count,s.run_epoch,s.allocated_at
 FROM thresholds t JOIN scoped_allocations s ON s.scope_kind=t.scope_kind AND s.tenant_id=t.tenant_id
  AND s.principal_ref=t.principal_ref AND s.ordinal=t.target_count
 WHERE t.current_count>=t.target_count
), mismatch AS (
 SELECT e.scope_kind,e.tenant_id,e.principal_ref,e.threshold_percent FROM expected e
 LEFT JOIN pilot_capacity_alert_events a ON a.scope_kind=e.scope_kind AND a.scope_tenant_id=e.tenant_id
 AND a.scope_principal_ref=e.principal_ref AND a.threshold_percent=e.threshold_percent
 WHERE a.threshold_percent IS NULL OR a.first_crossing_count<>e.first_crossing_count
  OR a.crossing_run_epoch<>e.crossing_run_epoch OR a.crossed_at<>e.crossed_at
 UNION ALL
 SELECT a.scope_kind,a.scope_tenant_id,a.scope_principal_ref,a.threshold_percent FROM pilot_capacity_alert_events a
 LEFT JOIN expected e ON e.scope_kind=a.scope_kind AND e.tenant_id=a.scope_tenant_id
 AND e.principal_ref=a.scope_principal_ref AND e.threshold_percent=a.threshold_percent
 WHERE e.threshold_percent IS NULL
)
SELECT count(*) AS violation_count FROM mismatch;
