SELECT format('CREATE ROLE pulseops_grafana LOGIN PASSWORD %L', :'grafana_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pulseops_grafana')
\gexec

ALTER ROLE pulseops_grafana PASSWORD :'grafana_password';
GRANT CONNECT ON DATABASE :"database" TO pulseops_grafana;
GRANT USAGE ON SCHEMA public TO pulseops_grafana;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO pulseops_grafana;
ALTER DEFAULT PRIVILEGES FOR ROLE :"owner" IN SCHEMA public GRANT SELECT ON TABLES TO pulseops_grafana;
