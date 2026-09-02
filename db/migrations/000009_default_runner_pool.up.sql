INSERT INTO runner_pools(id, name, pool_type, labels)
VALUES ('00000000-0000-4000-8000-000000000001', 'standard', 'standard', '{}'::jsonb)
ON CONFLICT (name) DO NOTHING;
