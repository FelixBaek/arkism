-- No raw token is stored. Preserve an unknown legacy issuance time honestly,
-- and give existing deployments 30 days from migration to rotate credentials.
CREATE TABLE arkism.principal_token_history (
 token_sha256 bytea PRIMARY KEY CHECK (octet_length(token_sha256)=32),
 principal_id text NOT NULL REFERENCES arkism.principals(id) ON DELETE CASCADE,
 issued_at timestamptz,
 expires_at timestamptz NOT NULL,
 rotated_from bytea,
 UNIQUE(token_sha256,principal_id),
 FOREIGN KEY(rotated_from,principal_id) REFERENCES arkism.principal_token_history(token_sha256,principal_id),
 CHECK (issued_at IS NULL OR
   (expires_at>issued_at AND expires_at<=issued_at+interval '2160 hours'))
);
INSERT INTO arkism.principal_token_history(token_sha256,principal_id,issued_at,expires_at)
SELECT token_sha256,id,NULL,statement_timestamp()+interval '720 hours' FROM arkism.principals;
ALTER TABLE arkism.principals ADD CONSTRAINT principal_current_token
 FOREIGN KEY(token_sha256,id) REFERENCES arkism.principal_token_history(token_sha256,principal_id)
 DEFERRABLE INITIALLY DEFERRED;

ALTER TABLE arkism.principal_token_history ENABLE ROW LEVEL SECURITY;
ALTER TABLE arkism.principal_token_history FORCE ROW LEVEL SECURITY;
CREATE POLICY arkism_init_owner ON arkism.principal_token_history FOR ALL TO PUBLIC
 USING (current_user='arkism_init') WITH CHECK (current_user='arkism_init');
REVOKE ALL ON arkism.principal_token_history FROM PUBLIC;
DO $$ DECLARE role_name text; BEGIN
 FOREACH role_name IN ARRAY ARRAY['anon','authenticated','service_role','arkism_app'] LOOP
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname=role_name) THEN
   EXECUTE format('REVOKE ALL ON arkism.principal_token_history FROM %I',role_name);
  END IF;
 END LOOP;
END $$;
CREATE TRIGGER recovery_write_gate BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE
 ON arkism.principal_token_history FOR EACH STATEMENT
 EXECUTE FUNCTION arkism.check_recovery_write_gate();

-- Each SQL statement rechecks expiry, even when a caller keeps a transaction
-- open. current_timestamp would freeze the clock at transaction start.
CREATE OR REPLACE FUNCTION arkism.current_principal() RETURNS TABLE(id text, role text, active boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT p.id,p.role,p.active FROM arkism.principals p
 JOIN arkism.principal_token_history t ON t.token_sha256=p.token_sha256 AND t.principal_id=p.id
 WHERE p.active AND encode(p.token_sha256,'hex')=current_setting('arkism.token_hash',true)
 AND t.expires_at>statement_timestamp()
 AND (t.issued_at IS NULL OR t.issued_at<=statement_timestamp()) LIMIT 1
$$;

CREATE OR REPLACE FUNCTION arkism.has_permission(org text, project text, requested_action text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT EXISTS (
  SELECT 1 FROM arkism.current_principal() p
  JOIN arkism.organization_memberships o ON o.principal_id=p.id AND o.organization_id=org AND o.active
  JOIN arkism.project_memberships m ON m.principal_id=p.id AND m.organization_id=org AND m.project_id=project AND m.active
  JOIN arkism.principal_permissions x ON x.principal_id=p.id AND x.organization_id=org AND x.project_id=project AND x.allowed
  WHERE x.action IN (requested_action,'*')
  AND (org NOT IN ('101-internal','__memory_blobs__','__unparsed__') OR p.role='service')
 )
$$;
CREATE OR REPLACE FUNCTION arkism.has_any_permission(requested_action text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT EXISTS (
  SELECT 1 FROM arkism.current_principal() p
  JOIN arkism.organization_memberships o ON o.principal_id=p.id AND o.active
  JOIN arkism.project_memberships m ON m.principal_id=p.id AND m.organization_id=o.organization_id AND m.active
  JOIN arkism.principal_permissions x ON x.principal_id=p.id AND x.organization_id=m.organization_id AND x.project_id=m.project_id AND x.allowed
  WHERE x.action IN (requested_action,'*')
 )
$$;
CREATE OR REPLACE FUNCTION arkism.permitted_scopes(requested_action text) RETURNS TABLE(org text, project text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT DISTINCT m.organization_id, m.project_id FROM arkism.current_principal() p
 JOIN arkism.organization_memberships o ON o.principal_id=p.id AND o.active
 JOIN arkism.project_memberships m ON m.principal_id=p.id AND m.organization_id=o.organization_id AND m.active
 JOIN arkism.principal_permissions x ON x.principal_id=p.id AND x.organization_id=m.organization_id AND x.project_id=m.project_id AND x.allowed
 WHERE x.action IN (requested_action,'*')
 AND (m.organization_id NOT IN ('101-internal','__memory_blobs__','__unparsed__') OR p.role='service')
$$;
CREATE OR REPLACE FUNCTION arkism.authorize_principal(org text, project text, requested_action text)
RETURNS TABLE(principal_id text, principal_role text, allowed boolean)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
 SELECT p.id,p.role,arkism.has_permission(org,project,requested_action)
 FROM arkism.current_principal() p
$$;
