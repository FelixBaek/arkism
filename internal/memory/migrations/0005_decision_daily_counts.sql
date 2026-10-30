-- Decision counts that outlive audit retention (#466). A decision is an audit_events row whose kind starts
-- with 'decision:'; this trigger adds it to a per-day count in the same transaction. Retention still deletes
-- the rows and the free-text reasons in them; the counts carry no text but the kind and reason code.
CREATE TABLE arkism.decision_daily_counts (
 workspace_id text NOT NULL, project_id text NOT NULL, day date NOT NULL,
 kind text NOT NULL, reason_code text NOT NULL DEFAULT '', count bigint NOT NULL CHECK (count > 0),
 PRIMARY KEY(workspace_id,project_id,day,kind,reason_code));

ALTER TABLE arkism.decision_daily_counts ENABLE ROW LEVEL SECURITY;
ALTER TABLE arkism.decision_daily_counts FORCE ROW LEVEL SECURITY;
CREATE POLICY arkism_init_owner ON arkism.decision_daily_counts FOR ALL TO PUBLIC
 USING (current_user='arkism_init') WITH CHECK (current_user='arkism_init');
-- Read like the audit rows they count; only the trigger below writes.
CREATE POLICY arkism_read ON arkism.decision_daily_counts FOR SELECT TO PUBLIC USING (
 (workspace_id,project_id) IN (SELECT org,project FROM arkism.permitted_scopes('db:read')));
REVOKE ALL ON arkism.decision_daily_counts FROM PUBLIC;
DO $$ DECLARE role_name text; BEGIN
 FOREACH role_name IN ARRAY ARRAY['anon','authenticated','service_role','arkism_app'] LOOP
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
   EXECUTE format('REVOKE ALL ON arkism.decision_daily_counts FROM %I',role_name);
  END IF;
 END LOOP;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='arkism_app') THEN
  GRANT SELECT ON arkism.decision_daily_counts TO arkism_app;
 END IF;
END $$;
CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE
 ON arkism.decision_daily_counts FOR EACH STATEMENT
 EXECUTE FUNCTION arkism.check_recovery_write_gate();

CREATE FUNCTION arkism.count_decision() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
BEGIN
 INSERT INTO arkism.decision_daily_counts AS c(workspace_id,project_id,day,kind,reason_code,count)
 VALUES(NEW.workspace_id,NEW.project_id,(COALESCE(NEW.updated_at,statement_timestamp()) AT TIME ZONE 'UTC')::date,
  NEW.kind,left(COALESCE(NEW.doc->'audit'->>'reason_code',''),64),1)
 ON CONFLICT (workspace_id,project_id,day,kind,reason_code) DO UPDATE SET count=c.count+1;
 RETURN NULL;
END $$;
REVOKE ALL ON FUNCTION arkism.count_decision() FROM PUBLIC;
CREATE TRIGGER count_decision AFTER INSERT ON arkism.memory_records FOR EACH ROW
 WHEN (NEW.collection='audit_events' AND NEW.kind LIKE 'decision:%')
 EXECUTE FUNCTION arkism.count_decision();
