-- v2.0-alpha — drop the DoA delegation table.
DROP INDEX IF EXISTS _doa_delegations_tenant_idx;
DROP INDEX IF EXISTS _doa_delegations_delegator_idx;
DROP INDEX IF EXISTS _doa_delegations_delegatee_idx;
DROP TABLE IF EXISTS _doa_delegations;
