package repository

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"cc-053/internal/models"
)

// 申诉台账的几种确定性失败，handler 据此映射 HTTP 状态码。
var (
	ErrAppealNotFound        = errors.New("appeal not found")
	ErrAppealNotPending      = errors.New("appeal is not pending")
	ErrReviewerIsDecider     = errors.New("reviewer cannot be the one who made the rejection decision")
	ErrRecordingNotRejected  = errors.New("recording is not in rejected state")
	ErrAppealPendingConflict = errors.New("an appeal is already pending for this recording")
)

type AppealRepo struct {
	db *sql.DB
}

func NewAppealRepo(db *sql.DB) *AppealRepo {
	return &AppealRepo{db: db}
}

const appealColumns = `id, appeal_no, recording_id, appellant, reason, status, deadline_at,
	prev_status, reject_reason, rejected_by, reviewer, review_note, reviewed_at,
	(status='pending' AND deadline_at < NOW()) AS is_overdue, created_at, updated_at`

func scanAppeal(row interface{ Scan(...interface{}) error }) (*models.Appeal, error) {
	a := &models.Appeal{}
	err := row.Scan(&a.ID, &a.AppealNo, &a.RecordingID, &a.Appellant, &a.Reason, &a.Status, &a.DeadlineAt,
		&a.PrevStatus, &a.RejectReason, &a.RejectedBy, &a.Reviewer, &a.ReviewNote, &a.ReviewedAt,
		&a.IsOverdue, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// Create 登记申诉单并写入首条处理轨迹（filed），同一事务提交。
// 单号由 appeal_no_seq 生成：APyyyymmdd-NNNN。
func (r *AppealRepo) Create(a *models.Appeal) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	a.Status = "pending"
	err = tx.QueryRow(
		`INSERT INTO appeals (appeal_no, recording_id, appellant, reason, status, deadline_at, prev_status, reject_reason, rejected_by)
		 VALUES ('AP' || to_char(NOW(), 'YYYYMMDD') || '-' || lpad(nextval('appeal_no_seq')::text, 4, '0'),
		         $1, $2, $3, 'pending', $4, $5, $6, $7)
		 RETURNING id, appeal_no, created_at, updated_at`,
		a.RecordingID, a.Appellant, a.Reason, a.DeadlineAt, a.PrevStatus, a.RejectReason, a.RejectedBy,
	).Scan(&a.ID, &a.AppealNo, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAppealPendingConflict
		}
		return err
	}

	if _, err := tx.Exec(
		`INSERT INTO appeal_events (appeal_id, actor, action, detail) VALUES ($1, $2, 'filed', $3)`,
		a.ID, a.Appellant, a.Reason,
	); err != nil {
		return err
	}

	return tx.Commit()
}

func (r *AppealRepo) GetByID(id int64) (*models.Appeal, error) {
	a, err := scanAppeal(r.db.QueryRow(`SELECT `+appealColumns+` FROM appeals WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, ErrAppealNotFound
	}
	return a, err
}

// CountByRecording 统计一条录音累计申诉次数（含已办结），用于次数上限。
func (r *AppealRepo) CountByRecording(recordingID int64) (int, error) {
	var count int
	err := r.db.QueryRow(`SELECT COUNT(*) FROM appeals WHERE recording_id=$1`, recordingID).Scan(&count)
	return count, err
}

// HasPending 是否已有待受理的申诉单。
func (r *AppealRepo) HasPending(recordingID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM appeals WHERE recording_id=$1 AND status='pending')`, recordingID).Scan(&exists)
	return exists, err
}

// List 台账列表：按录音/状态/是否逾期过滤，逾期单排在最前，并返回逾期总数。
func (r *AppealRepo) List(query models.AppealQuery) ([]*models.Appeal, int, int, error) {
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

	var total, overdueCount int
	if err := r.db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM appeals %s", whereClause), args...).Scan(&total); err != nil {
		return nil, 0, 0, err
	}
	// 逾期数不受 overdue 过滤影响，始终反映全量台账里待受理且超期的单量
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM appeals WHERE status='pending' AND deadline_at < NOW()`).Scan(&overdueCount); err != nil {
		return nil, 0, 0, err
	}

	dataQuery := fmt.Sprintf(
		`SELECT %s FROM appeals %s
		 ORDER BY (status='pending' AND deadline_at < NOW()) DESC, id
		 LIMIT $%d OFFSET $%d`,
		appealColumns, whereClause, argIdx, argIdx+1,
	)
	args = append(args, query.Limit, query.Offset)

	rows, err := r.db.Query(dataQuery, args...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()

	var appeals []*models.Appeal
	for rows.Next() {
		a, err := scanAppeal(rows)
		if err != nil {
			return nil, 0, 0, err
		}
		appeals = append(appeals, a)
	}
	return appeals, total, overdueCount, nil
}

// Review 复检裁决：维持（upheld）或推翻（overturned）。
// 复检人不能是当初下退回结论的人；推翻时把录音恢复到退回前状态。
// 申诉单更新、处理轨迹、录音恢复在同一事务内完成。
func (r *AppealRepo) Review(appealID int64, reviewer, decision, note string) (*models.Appeal, error) {
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var status, rejectedBy string
	var recordingID int64
	err = tx.QueryRow(
		`SELECT status, rejected_by, recording_id FROM appeals WHERE id=$1 FOR UPDATE`, appealID,
	).Scan(&status, &rejectedBy, &recordingID)
	if err == sql.ErrNoRows {
		return nil, ErrAppealNotFound
	}
	if err != nil {
		return nil, err
	}
	if status != "pending" {
		return nil, ErrAppealNotPending
	}
	if rejectedBy != "" && reviewer == rejectedBy {
		return nil, ErrReviewerIsDecider
	}

	if _, err := tx.Exec(
		`UPDATE appeals SET status=$2, reviewer=$3, review_note=$4, reviewed_at=NOW(), updated_at=NOW() WHERE id=$1`,
		appealID, decision, reviewer, note,
	); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(
		`INSERT INTO appeal_events (appeal_id, actor, action, detail) VALUES ($1, $2, $3, $4)`,
		appealID, reviewer, decision, note,
	); err != nil {
		return nil, err
	}

	if decision == "overturned" {
		// 录音回到退回之前的样子：恢复 prev_status，清掉退回结论
		result, err := tx.Exec(
			`UPDATE recordings SET status=prev_status, prev_status='', reject_reason='', rejected_by='', rejected_at=NULL, updated_at=NOW()
			 WHERE id=$1 AND status='rejected'`,
			recordingID,
		)
		if err != nil {
			return nil, err
		}
		if rows, _ := result.RowsAffected(); rows == 0 {
			return nil, ErrRecordingNotRejected
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r.GetByID(appealID)
}

// ListEvents 处理轨迹：每一次处理是谁、在什么时候、做了什么。
func (r *AppealRepo) ListEvents(appealID int64) ([]*models.AppealEvent, error) {
	rows, err := r.db.Query(
		`SELECT id, appeal_id, actor, action, detail, created_at
		 FROM appeal_events WHERE appeal_id=$1 ORDER BY id`, appealID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []*models.AppealEvent
	for rows.Next() {
		e := &models.AppealEvent{}
		if err := rows.Scan(&e.ID, &e.AppealID, &e.Actor, &e.Action, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "idx_appeals_one_pending")
}
