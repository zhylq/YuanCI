-- Existing proofs are short-lived authorizations, not project data. Invalidate
-- them so no old proof can survive without an explicit verified identity.
DELETE FROM github_import_proofs;
ALTER TABLE github_import_proofs ADD COLUMN github_subject text NOT NULL;
