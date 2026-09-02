DELETE FROM runner_pools
WHERE id = '00000000-0000-4000-8000-000000000001'
  AND NOT EXISTS (SELECT 1 FROM runners WHERE pool_id = runner_pools.id)
  AND NOT EXISTS (SELECT 1 FROM runner_registration_tokens WHERE pool_id = runner_pools.id);
