ALTER TABLE monitors DROP CONSTRAINT monitors_monitor_type_check;
ALTER TABLE monitors ADD CONSTRAINT monitors_monitor_type_check CHECK (monitor_type IN ('http', 'heartbeat', 'tcp', 'dns'));
