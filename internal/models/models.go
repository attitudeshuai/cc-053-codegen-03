package models

import (
	"time"
)

type Speaker struct {
	ID               int64     `json:"id"`
	CodeName         string    `json:"code_name"`
	BirthYear        int       `json:"birth_year"`
	Gender           string    `json:"gender"`
	DialectPointCode string    `json:"dialect_point_code"`
	Occupation       string    `json:"occupation"`
	YearsAway        int       `json:"years_away"`
	ContactRef       string    `json:"contact_ref,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type Wordlist struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	Entries   []Entry   `json:"entries"`
	IsCurrent bool      `json:"is_current"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Entry struct {
	ID     int64  `json:"id"`
	Hanzi  string `json:"hanzi"`
	Gloss  string `json:"gloss"`
	IPARef string `json:"ipa_ref,omitempty"`
	Group  string `json:"group,omitempty"`
}

type Task struct {
	ID         int64     `json:"id"`
	WordlistID int64     `json:"wordlist_id"`
	SpeakerID  int64     `json:"speaker_id"`
	Kind       string    `json:"kind"` // record | annotate
	Assignee   string    `json:"assignee"`
	Status     string    `json:"status"` // pending | in_progress | completed | failed
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Recording struct {
	ID           int64      `json:"id"`
	TaskID       int64      `json:"task_id"`
	ObjectKey    string     `json:"object_key"`
	DurationMs   int        `json:"duration_ms"`
	SampleRate   int        `json:"sample_rate"`
	PeakDB       float64    `json:"peak_db"`
	Device       string     `json:"device"`
	RecordedAt   *time.Time `json:"recorded_at"`
	Status       string     `json:"status"` // pending | processing | completed | rejected
	RejectReason string     `json:"reject_reason,omitempty"`
	PrevStatus   string     `json:"prev_status,omitempty"`   // 退回前的状态，申诉推翻后恢复用
	RejectedBy   string     `json:"rejected_by,omitempty"`   // 下退回结论的人（自动质检为 system）
	RejectedAt   *time.Time `json:"rejected_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type Segment struct {
	ID          int64     `json:"id"`
	RecordingID int64     `json:"recording_id"`
	EntryID     int64     `json:"entry_id"`
	StartMs     int       `json:"start_ms"`
	EndMs       int       `json:"end_ms"`
	ObjectKey   string    `json:"object_key"`
	SnrDB       float64   `json:"snr_db"`
	Status      string    `json:"status"` // pending | annotated | in_arbitration | completed | failed
	Version     int       `json:"version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Annotation struct {
	ID        int64     `json:"id"`
	SegmentID int64     `json:"segment_id"`
	Annotator string    `json:"annotator"`
	IPA       string    `json:"ipa"`
	Tone      string    `json:"tone"`
	Note      string    `json:"note"`
	Decision  string    `json:"decision"` // pending | accept | reject | arbitrated
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Arbitration struct {
	ID                 int64     `json:"id"`
	SegmentID          int64     `json:"segment_id"`
	WinnerAnnotationID int64     `json:"winner_annotation_id"`
	Arbiter            string    `json:"arbiter"`
	Reason             string    `json:"reason"`
	CreatedAt          time.Time `json:"created_at"`
}

type ExportJob struct {
	ID          int64     `json:"id"`
	Filter      string    `json:"filter"`
	Status      string    `json:"status"` // pending | processing | completed | failed
	Progress    int       `json:"progress"`
	OutputKey   string    `json:"output_key"`
	ErrorMessage string   `json:"error_message,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Appeal 申诉台账：提交方对录音退回结论不服时发起，限期受理、复检、留痕。
type Appeal struct {
	ID           int64      `json:"id"`
	AppealNo     string     `json:"appeal_no"`   // 申诉单号，如 AP20260918-0001
	RecordingID  int64      `json:"recording_id"`
	Appellant    string     `json:"appellant"`   // 提交申诉的人
	Reason       string     `json:"reason"`      // 申诉理由
	Status       string     `json:"status"`      // pending | upheld(维持) | overturned(推翻)
	DeadlineAt   time.Time  `json:"deadline_at"` // 受理期限，超时未处理即逾期
	PrevStatus   string     `json:"prev_status"` // 退回前状态快照（推翻后恢复）
	RejectReason string     `json:"reject_reason,omitempty"` // 被申诉的退回结论快照
	RejectedBy   string     `json:"rejected_by,omitempty"`   // 当初下结论的人，复检人不得与其相同
	Reviewer     string     `json:"reviewer,omitempty"`
	ReviewNote   string     `json:"review_note,omitempty"`   // 复检依据（维持/推翻必填）
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
	IsOverdue    bool       `json:"is_overdue"` // pending 且已过 deadline_at
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// AppealEvent 申诉处理轨迹：谁在什么时候做了什么。
type AppealEvent struct {
	ID        int64     `json:"id"`
	AppealID  int64     `json:"appeal_id"`
	Actor     string    `json:"actor"`
	Action    string    `json:"action"` // filed | upheld | overturned
	Detail    string    `json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

// Pagination
type Pagination struct {
	Offset int `form:"offset" json:"offset"`
	Limit  int `form:"limit" json:"limit"`
}

func (p *Pagination) Normalize() {
	if p.Limit <= 0 || p.Limit > 100 {
		p.Limit = 20
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
}