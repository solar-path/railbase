-- Reverse of 0034_doa_slice0_prototype.up.sql.
-- Drop order: children before parents (workflow_decisions and matrix_approvers
-- cascade via ON DELETE CASCADE, but explicit DROPs are safer + idempotent).

DROP TABLE IF EXISTS _doa_workflow_decisions;
DROP TABLE IF EXISTS _doa_workflows;
DROP TABLE IF EXISTS _doa_matrix_approvers;
DROP TABLE IF EXISTS _doa_matrix_levels;
DROP TABLE IF EXISTS _doa_matrices;
