CREATE ROLE ao_cloud_owner
    LOGIN
    PASSWORD 'ao_cloud_local_owner'
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE
    NOINHERIT
    NOBYPASSRLS;

CREATE ROLE ao_cloud_app
    LOGIN
    PASSWORD 'ao_cloud_local_app'
    NOSUPERUSER
    NOCREATEDB
    NOCREATEROLE
    NOINHERIT
    NOBYPASSRLS;

ALTER DATABASE ao_cloud OWNER TO ao_cloud_owner;
ALTER SCHEMA public OWNER TO ao_cloud_owner;

-- The image bootstrap role is needed only while this script runs.
ALTER ROLE ao_cloud_bootstrap NOLOGIN;
