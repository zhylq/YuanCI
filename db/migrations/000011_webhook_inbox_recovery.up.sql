-- Deliveries created before the normalized inbox existed cannot be replayed
-- safely because their raw payload was deliberately not retained.
UPDATE webhook_deliveries
SET status = 'ignored',
    processed_at = COALESCE(processed_at, clock_timestamp()),
    error_code = COALESCE(error_code, 'legacy_payload_unavailable'),
    error_summary = COALESCE(error_summary, 'Delivery predates the normalized webhook inbox')
WHERE normalized_event IS NULL AND status = 'received';
