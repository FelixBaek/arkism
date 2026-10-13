-- Roles and schemas for Arkism (Docker server setup and the app's built-in Postgres). Safe to run again.
-- psql variables: init_pw, app_pw
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'arkism_init') THEN
    CREATE ROLE arkism_init LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  END IF;
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'arkism_app') THEN
    CREATE ROLE arkism_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  END IF;
END $$;
ALTER ROLE arkism_init PASSWORD :'init_pw';
ALTER ROLE arkism_app PASSWORD :'app_pw';
REVOKE ALL ON DATABASE arkism FROM PUBLIC;
GRANT CONNECT ON DATABASE arkism TO arkism_init, arkism_app;
GRANT CREATE ON DATABASE arkism TO arkism_init;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA IF NOT EXISTS extensions AUTHORIZATION CURRENT_USER;
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA extensions;
CREATE SCHEMA IF NOT EXISTS arkism AUTHORIZATION arkism_init;
ALTER SCHEMA arkism OWNER TO arkism_init;
REVOKE ALL ON SCHEMA arkism FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA arkism TO arkism_init;
GRANT USAGE ON SCHEMA extensions TO arkism_init;
GRANT USAGE ON SCHEMA arkism, extensions TO arkism_app;
ALTER DEFAULT PRIVILEGES FOR ROLE arkism_init IN SCHEMA arkism GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO arkism_app;
ALTER DEFAULT PRIVILEGES FOR ROLE arkism_init IN SCHEMA arkism GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO arkism_app;
ALTER ROLE arkism_init IN DATABASE arkism SET search_path = arkism, extensions, pg_catalog;
ALTER ROLE arkism_app IN DATABASE arkism SET search_path = arkism, extensions, pg_catalog;
