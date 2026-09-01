package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/yuanci/yuanci/internal/runnerauth"
)

func (s *Store) CreateRegistrationToken(ctx context.Context, record runnerauth.RegistrationToken) error {
	if record.ID == uuid.Nil || record.PoolName == "" || record.MaxUses < 1 {
		return runnerauth.ErrInvalidInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var poolID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM runner_pools WHERE name=$1 FOR SHARE`, record.PoolName).Scan(&poolID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return runnerauth.ErrInvalidInput
		}
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO runner_registration_tokens
        (id,pool_id,token_digest,expires_at,max_uses,created_by) VALUES($1,$2,$3,$4,$5,$6)`,
		record.ID, poolID, record.Digest[:], record.ExpiresAt, record.MaxUses, record.CreatedBy)
	if err != nil {
		return fmt.Errorf("create Runner registration token: %w", err)
	}
	if err := appendRunnerAudit(ctx, tx, record.CreatedBy, "runner_registration_token.created", "runner_registration_token", record.ID,
		map[string]any{"pool_id": poolID, "expires_at": record.ExpiresAt, "max_uses": record.MaxUses}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) EnrollRunner(ctx context.Context, enrollment runnerauth.Enrollment) (runnerauth.Identity, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return runnerauth.Identity{}, err
	}
	defer tx.Rollback(ctx)
	var tokenID, poolID uuid.UUID
	var poolType string
	err = tx.QueryRow(ctx, `SELECT token.id,token.pool_id,pool.pool_type
        FROM runner_registration_tokens token JOIN runner_pools pool ON pool.id=token.pool_id
        WHERE token.token_digest=$1 AND token.revoked_at IS NULL
          AND token.expires_at > clock_timestamp() AND token.used_count < token.max_uses
        FOR UPDATE OF token`, enrollment.TokenDigest[:]).Scan(&tokenID, &poolID, &poolType)
	if errors.Is(err, pgx.ErrNoRows) {
		return runnerauth.Identity{}, runnerauth.ErrDenied
	}
	if err != nil {
		return runnerauth.Identity{}, err
	}
	if poolType != enrollment.Capabilities.IsolationLevel {
		return runnerauth.Identity{}, runnerauth.ErrDenied
	}
	labels, err := json.Marshal(enrollment.Capabilities.Labels)
	if err != nil {
		return runnerauth.Identity{}, runnerauth.ErrDenied
	}
	_, err = tx.Exec(ctx, `INSERT INTO runners
        (id,pool_id,name,status,capacity,labels,certificate_serial,os,architecture,executor,
         isolation_level,available_disk_bytes,protocol_version,runner_version)
        VALUES($1,$2,$3,'offline',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		enrollment.RunnerID, poolID, enrollment.Name, enrollment.Capabilities.Capacity, labels,
		enrollment.Certificate.Serial, enrollment.Capabilities.OS, enrollment.Capabilities.Architecture,
		enrollment.Capabilities.Executor, enrollment.Capabilities.IsolationLevel,
		enrollment.Capabilities.AvailableDiskBytes, enrollment.Capabilities.ProtocolVersion,
		enrollment.Capabilities.RunnerVersion)
	if err != nil {
		return runnerauth.Identity{}, runnerauth.ErrDenied
	}
	certificateID := uuid.New()
	if err := insertRunnerCertificate(ctx, tx, certificateID, enrollment.RunnerID, enrollment.Certificate, nil); err != nil {
		return runnerauth.Identity{}, err
	}
	result, err := tx.Exec(ctx, `UPDATE runner_registration_tokens SET used_count=used_count+1,
        last_used_at=clock_timestamp(),last_used_runner_id=$2 WHERE id=$1 AND used_count < max_uses`, tokenID, enrollment.RunnerID)
	if err != nil || result.RowsAffected() != 1 {
		return runnerauth.Identity{}, runnerauth.ErrDenied
	}
	if err := appendRunnerAudit(ctx, tx, nil, "runner.enrolled", "runner", enrollment.RunnerID,
		map[string]any{"pool_id": poolID, "certificate_serial": enrollment.Certificate.Serial, "registration_token_id": tokenID}); err != nil {
		return runnerauth.Identity{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return runnerauth.Identity{}, err
	}
	return runnerauth.Identity{RunnerID: enrollment.RunnerID, PoolID: poolID, PoolType: poolType,
		Name: enrollment.Name, CertificateID: certificateID, Serial: enrollment.Certificate.Serial,
		Capabilities: enrollment.Capabilities}, nil
}

func (s *Store) AuthenticateRunner(ctx context.Context, runnerID uuid.UUID, serial string) (runnerauth.Identity, error) {
	var identity runnerauth.Identity
	var labels []byte
	err := s.pool.QueryRow(ctx, `SELECT runner.id,runner.pool_id,pool.pool_type,runner.name,certificate.id,
        certificate.serial,runner.os,runner.architecture,runner.executor,runner.isolation_level,
        runner.capacity,runner.available_disk_bytes,runner.protocol_version,runner.runner_version,runner.labels
        FROM runner_certificates certificate
        JOIN runners runner ON runner.id=certificate.runner_id
        JOIN runner_pools pool ON pool.id=runner.pool_id
        WHERE certificate.serial=$1 AND certificate.runner_id=$2 AND runner.status <> 'disabled'
          AND certificate.not_before <= clock_timestamp() AND certificate.not_after > clock_timestamp()
          AND (certificate.state='active' OR
               (certificate.state='retiring' AND certificate.retire_at > clock_timestamp()))`, serial, runnerID).Scan(
		&identity.RunnerID, &identity.PoolID, &identity.PoolType, &identity.Name, &identity.CertificateID,
		&identity.Serial, &identity.Capabilities.OS, &identity.Capabilities.Architecture,
		&identity.Capabilities.Executor, &identity.Capabilities.IsolationLevel, &identity.Capabilities.Capacity,
		&identity.Capabilities.AvailableDiskBytes, &identity.Capabilities.ProtocolVersion,
		&identity.Capabilities.RunnerVersion, &labels)
	if errors.Is(err, pgx.ErrNoRows) {
		return runnerauth.Identity{}, runnerauth.ErrDenied
	}
	if err != nil {
		return runnerauth.Identity{}, err
	}
	if err := json.Unmarshal(labels, &identity.Capabilities.Labels); err != nil {
		return runnerauth.Identity{}, runnerauth.ErrDenied
	}
	return identity, nil
}

func (s *Store) RotateRunnerCertificate(ctx context.Context, rotation runnerauth.Rotation) (runnerauth.CertificateRecord, error) {
	if rotation.GracePeriod <= 0 || rotation.GracePeriod > runnerauth.RotationGracePeriod {
		return runnerauth.CertificateRecord{}, runnerauth.ErrDenied
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return runnerauth.CertificateRecord{}, err
	}
	defer tx.Rollback(ctx)
	var oldID uuid.UUID
	var oldState string
	var oldRetireAt *time.Time
	err = tx.QueryRow(ctx, `SELECT certificate.id,certificate.state,certificate.retire_at
        FROM runner_certificates certificate JOIN runners runner ON runner.id=certificate.runner_id
        WHERE certificate.serial=$1 AND certificate.runner_id=$2 AND runner.status <> 'disabled'
        FOR UPDATE OF certificate,runner`, rotation.OldSerial, rotation.RunnerID).Scan(&oldID, &oldState, &oldRetireAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return runnerauth.CertificateRecord{}, runnerauth.ErrDenied
	}
	if err != nil {
		return runnerauth.CertificateRecord{}, err
	}
	if existing, found, err := replacementCertificate(ctx, tx, oldID); err != nil {
		return runnerauth.CertificateRecord{}, err
	} else if found {
		if !bytes.Equal(existing.CSRFingerprint[:], rotation.Certificate.CSRFingerprint[:]) {
			return runnerauth.CertificateRecord{}, runnerauth.ErrDenied
		}
		return existing, nil
	}
	if oldState != "active" {
		return runnerauth.CertificateRecord{}, runnerauth.ErrDenied
	}
	result, err := tx.Exec(ctx, `UPDATE runner_certificates SET state='retiring',
        retire_at=LEAST(not_after,clock_timestamp()+make_interval(secs => $2))
		WHERE id=$1 AND state='active'`, oldID, rotation.GracePeriod.Seconds())
	if err != nil || result.RowsAffected() != 1 {
		return runnerauth.CertificateRecord{}, runnerauth.ErrDenied
	}
	newID := uuid.New()
	if err := insertRunnerCertificate(ctx, tx, newID, rotation.RunnerID, rotation.Certificate, &oldID); err != nil {
		return runnerauth.CertificateRecord{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE runners SET certificate_serial=$2 WHERE id=$1`, rotation.RunnerID, rotation.Certificate.Serial); err != nil {
		return runnerauth.CertificateRecord{}, err
	}
	if err := appendRunnerAudit(ctx, tx, nil, "runner_certificate.rotated", "runner", rotation.RunnerID,
		map[string]any{"old_serial": rotation.OldSerial, "new_serial": rotation.Certificate.Serial, "grace_seconds": int(rotation.GracePeriod.Seconds())}); err != nil {
		return runnerauth.CertificateRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return runnerauth.CertificateRecord{}, err
	}
	return rotation.Certificate, nil
}

func (s *Store) DisableRunner(ctx context.Context, runnerID uuid.UUID, reason string, actor *uuid.UUID) error {
	if runnerID == uuid.Nil || len(reason) < 1 || len(reason) > 256 {
		return runnerauth.ErrInvalidInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, `UPDATE runners SET status='disabled',disabled_reason=$2,
        certificate_serial=NULL WHERE id=$1 AND status <> 'disabled'`, runnerID, reason)
	if err != nil || result.RowsAffected() != 1 {
		return runnerauth.ErrDenied
	}
	if _, err := tx.Exec(ctx, `UPDATE runner_certificates SET state='revoked',retire_at=NULL,
        revoked_at=clock_timestamp(),revocation_reason=$2 WHERE runner_id=$1 AND state IN ('active','retiring')`, runnerID, reason); err != nil {
		return err
	}
	if err := appendRunnerAudit(ctx, tx, actor, "runner.disabled", "runner", runnerID, map[string]any{"reason": reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RevokeRunnerCertificate(ctx context.Context, serial, reason string, actor *uuid.UUID) error {
	if len(serial) < 16 || len(serial) > 64 || len(reason) < 1 || len(reason) > 256 {
		return runnerauth.ErrInvalidInput
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var runnerID uuid.UUID
	err = tx.QueryRow(ctx, `UPDATE runner_certificates SET state='revoked',retire_at=NULL,
        revoked_at=clock_timestamp(),revocation_reason=$2 WHERE serial=$1 AND state IN ('active','retiring')
        RETURNING runner_id`, serial, reason).Scan(&runnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return runnerauth.ErrDenied
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runners SET certificate_serial=NULL WHERE id=$1 AND certificate_serial=$2`, runnerID, serial); err != nil {
		return err
	}
	if err := appendRunnerAudit(ctx, tx, actor, "runner_certificate.revoked", "runner", runnerID,
		map[string]any{"serial": serial, "reason": reason}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func insertRunnerCertificate(ctx context.Context, tx pgx.Tx, id, runnerID uuid.UUID, certificate runnerauth.CertificateRecord, replaces *uuid.UUID) error {
	_, err := tx.Exec(ctx, `INSERT INTO runner_certificates
        (id,runner_id,serial,csr_fingerprint,public_key_fingerprint,state,certificate_chain_pem,
         not_before,not_after,replaces_certificate_id)
        VALUES($1,$2,$3,$4,$5,'active',$6,$7,$8,$9)`, id, runnerID, certificate.Serial,
		certificate.CSRFingerprint[:], certificate.PublicKeyFingerprint[:], certificate.ChainPEM,
		certificate.NotBefore, certificate.NotAfter, replaces)
	return err
}

func replacementCertificate(ctx context.Context, tx pgx.Tx, oldID uuid.UUID) (runnerauth.CertificateRecord, bool, error) {
	var record runnerauth.CertificateRecord
	var csr, public []byte
	err := tx.QueryRow(ctx, `SELECT serial,csr_fingerprint,public_key_fingerprint,certificate_chain_pem,
        not_before,not_after FROM runner_certificates WHERE replaces_certificate_id=$1`, oldID).Scan(
		&record.Serial, &csr, &public, &record.ChainPEM, &record.NotBefore, &record.NotAfter)
	if errors.Is(err, pgx.ErrNoRows) {
		return runnerauth.CertificateRecord{}, false, nil
	}
	if err != nil || len(csr) != 32 || len(public) != 32 {
		return runnerauth.CertificateRecord{}, false, err
	}
	copy(record.CSRFingerprint[:], csr)
	copy(record.PublicKeyFingerprint[:], public)
	return record, true, nil
}

func appendRunnerAudit(ctx context.Context, tx pgx.Tx, actor *uuid.UUID, action, resource string, id uuid.UUID, metadata map[string]any) error {
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_user_id,action,resource_type,resource_id,metadata)
        VALUES($1,$2,$3,$4,$5)`, actor, action, resource, id.String(), encoded)
	return err
}
