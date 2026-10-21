-- Deliberately retain existing search data and the model catalog on rollback.
-- Roll back the application release, not the shared RDS schema.
SELECT 1;
