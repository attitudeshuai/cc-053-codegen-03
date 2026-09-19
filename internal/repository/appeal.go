package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"cc-053/internal/models"
)

// ErrAppealNotPending 申诉已被处理（并发重复处理时由 WHERE status='pending' 兜底）。
var ErrAppealNotPending = errors.New("appeal is not pending")

type AppealRepo struct {
	db *sql.DB
}

func NewAppealRepo(db *sql.DB) *AppealRepo {
	return &AppealRepo{db: db}
}

const appealColumns = `id, ticket_no, recording_id, appellant, reason, status,
	original_rejected_by, original_reject_reason, deadline_at,
	(status='pending' AND deadline_at < NOW()) AS is_overdue,
	reviewer, decision_basis, resolved_at, created_at, updated_at`

func scanAppeal(row interface{ Scan(...interface{}) error }) (*models.Appeal, error) {
	a := &models.Appeal{}
	err := row.Scan(
		&a.ID, &a.TicketNo, &a.RecordingID, &a.Appellant, &a.Reason, &a.Status,
		&a.OriginalRejectedBy, &a.OriginalRejectReason, &a.DeadlineAt,
		&a.IsOverdue,
		&a.Reviewer, &a.DecisionBasis, &a.ResolvedAt, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// Create 建申诉单并写入「提交」留痕，同一事务。单号由序列生成：APyyyymmdd-000001。
func (r *AppealRepo) Create(a *models.Appeal) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	err = tx.QueryRow(
		`INSERT INTO appeals (ticket_no, recording_id, appellant, reason, status, original_rejected_by, original_reject_reason, deadline_at)
		 VALUES ('AP' || to_char(NOW(), 'YYYYMMDD') || '-' || lpad(nextval('appeal_ticket_seq')::text, 6, '0'),
		         $1, $2, $3, 'pending', $4, $5, $6)
		 RETURNING id, ticket_no, created_at, updated_at`,
		a.RecordingID, a.Appellant, a.Reason, a.OriginalRejectedBy, a.OriginalRejectReason, a.DeadlineAt,
	).Scan(&a.ID, &a.TicketNo, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(
		`INSERT INTO appeal_events (appeal_id, action, actor, detail) VALUES ($1, 'submitted', $2, $3)`,
		a.ID, a.Appellant, a.Reason,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (r *AppealRepo) GetByID(id int64) (*models.Appeal, error) {
	return scanAppeal(r.db.QueryRow(`SELECT `+appealColumns+` FROM appeals WHERE id=$1`, id))
}

// CountByRecording 该录音累计申诉次数（含已办结），用于次数上限。
func (r *AppealRepo) CountByRecording(recordingID int64) (int, error) {
	var count int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM appeals WHERE recording_id=$1`, recordingID).Scan(&count)
	return count, err
}

// HasPendingByRecording 同一录音同一时间只允许一单在办。
func (r *AppealRepo) HasPendingByRecording(recordingID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM appeals WHERE recording_id=$1 AND status='pending')`, recordingID,
	).Scan(&exists)
	return exists, err
}

// CountOverdue 当前超期未处理的申诉数（台账顶部一眼可见）。
func (r *AppealRepo) CountOverdue() (int, error) {
	var count int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM appeals WHERE status='pending' AND deadline_at < NOW()`).Scan(&count)
	return count, err
}

// List 台账列表：超期未办的排最前，其余按受理期限升序。
func (r *AppealRepo) List(query models.AppealQuery) ([]*models.Appeal, int, error) {
	query.Normalize()

	var conditions []string
	var args []interface{}
	argIdx := 1

	if query.RecordingID > 0 {
		conditions = append(conditions, fmt.Sprintf("recording_id = $%d", argIdx))
		args = append(args, query.RecordingID)
		argIdx++
	}
	if query.Status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", argIdx))
		args = append(args, query.Status)
		argIdx++
	}
	if query.Overdue == "true" {
		conditions = append(conditions, "status = 'pending' AND deadline_at < NOW()")
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	var total int
	if err := r.db.QueryRow("SELECT COUNT(*) FROM appeals "+whereClause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	dataQuery := fmt.Sprintf(
		`SELECT %s FROM appeals %s
		 ORDER BY (status='pending' AND deadline_at < NOW()) DESC, deadline_at ASC, id ASC
		 LIMIT $%d OFFSET $%d`,
		appealColumns, whereClause, argIdx, argIdx+1,
	)
	args = append(args, query.Limit, query.Offset)

	rows, err := r.db.Query(dataQuery, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var appeals []*models.Appeal
	for rows.Next() {
		a, err := scanAppeal(rows)
		if err != nil {
			return nil, 0, err
		}
		appeals = append(appeals, a)
	}
	return appeals, total, nil
}

// Resolve 办结申诉：更新结论、（推翻时）把录音恢复到退回前状态、写留痕，同一事务。
// a.Status 为 upheld|overturned；overturned 时 restoreStatus 为录音退回前状态。
func (r *AppealRepo) Resolve(a *models.Appeal, restoreStatus string, eventDetail string) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`UPDATE appeals SET status=$1, reviewer=$2, decision_basis=$3, resolved_at=NOW(), updated_at=NOW()
		 WHERE id=$4 AND status='pending'`,
		a.Status, a.Reviewer, a.DecisionBasis, a.ID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrAppealNotPending
	}

	if a.Status == "overturned" {
		if _, err := tx.Exec(
			`UPDATE recordings SET status=$1, reject_reason='', rejected_by='', pre_reject_status='', updated_at=NOW() WHERE id=$2`,
			restoreStatus, a.RecordingID,
		); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(
		`INSERT INTO appeal_events (appeal_id, action, actor, detail) VALUES ($1, $2, $3, $4)`,
		a.ID, a.Status, a.Reviewer, eventDetail,
	); err != nil {
		return err
	}

	return tx.Commit()
}

// ListEvents 一单的全部处理留痕（谁在什么时候做了什么），按时间升序。
func (r *AppealRepo) ListEvents(appealID int64) ([]*models.AppealEvent, error) {
	rows, err := r.db.Query(
		`SELECT id, appeal_id, action, actor, detail, created_at
		 FROM appeal_events WHERE appeal_id=$1 ORDER BY id`, appealID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*models.AppealEvent
	for rows.Next() {
		e := &models.AppealEvent{}
		if err := rows.Scan(&e.ID, &e.AppealID, &e.Action, &e.Actor, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}
