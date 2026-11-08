-- Ordered per-kind streams let claim stop at the first available job. Keeping
-- status predicates literal also makes these indexes usable by generic plans.
CREATE INDEX memory_records_jobs_waiting_order
ON arkism.memory_records (kind,run_at,id)
WHERE collection='jobs' AND status IN ('pending','retry');

-- Lease expiry is eligibility, not priority. Preserve run_at NULLS LAST,id;
-- putting lease_until first would change which expired job is reclaimed.
CREATE INDEX memory_records_jobs_running_order
ON arkism.memory_records (kind,run_at,id,lease_until)
WHERE collection='jobs' AND status='running';

-- Privacy advancement, backup waiting and extraction-batch polling use this
-- order within one kind, including when their queue is empty.
CREATE INDEX memory_records_jobs_kind_updated
ON arkism.memory_records (kind,updated_at,id)
WHERE collection='jobs';
