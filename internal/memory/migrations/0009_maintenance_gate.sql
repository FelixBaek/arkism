CREATE TABLE arkism.memory_maintenance_gate (
 id integer PRIMARY KEY CHECK (id=1),
 revision bigint NOT NULL CHECK (revision>0),
 operation jsonb
);
INSERT INTO arkism.memory_maintenance_gate(id,revision) VALUES(1,1);
REVOKE ALL ON arkism.memory_maintenance_gate FROM PUBLIC;

-- Every repository transaction holds this shared lock through its final read /
-- write. Owner maintenance takes FOR UPDATE, draining prior admitted work.
CREATE FUNCTION arkism.check_maintenance_access() RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,arkism AS $$
DECLARE pending jsonb;
BEGIN
 SELECT operation INTO pending FROM arkism.memory_maintenance_gate WHERE id=1 FOR SHARE;
 IF NOT FOUND OR pending IS NOT NULL THEN
  RAISE EXCEPTION 'memory maintenance unavailable' USING ERRCODE='AK503';
 END IF;
END $$;
REVOKE ALL ON FUNCTION arkism.check_maintenance_access() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION arkism.check_maintenance_access() TO PUBLIC;
