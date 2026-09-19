package handlers

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"cc-053/internal/config"
	"cc-053/internal/models"
	"cc-053/internal/repository"
)

// AppealHandler 申诉台账：退回申诉的登记、查询与复检裁决。
type AppealHandler struct {
	appealRepo    *repository.AppealRepo
	recordingRepo *repository.RecordingRepo
	cfg           *config.Config
}

func NewAppealHandler(appealRepo *repository.AppealRepo, recordingRepo *repository.RecordingRepo, cfg *config.Config) *AppealHandler {
	return &AppealHandler{appealRepo: appealRepo, recordingRepo: recordingRepo, cfg: cfg}
}

// File POST /api/v1/recordings/:id/appeals — 提交方对退回结论提申诉。
func (h *AppealHandler) File(c *gin.Context) {
	recordingID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid recording id"})
		return
	}

	var req models.CreateAppealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid request", Detail: err.Error()})
		return
	}

	recording, err := h.recordingRepo.GetByID(recordingID)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "recording not found"})
		return
	}
	if recording.Status != "rejected" {
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "only rejected recordings can be appealed"})
		return
	}

	// 同一条录音申诉到次数上限就不再受理
	count, err := h.appealRepo.CountByRecording(recordingID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to count appeals", Detail: err.Error()})
		return
	}
	if count >= h.cfg.AppealMaxPerRecording {
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "appeal limit reached for this recording"})
		return
	}

	if pending, err := h.appealRepo.HasPending(recordingID); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to check pending appeals", Detail: err.Error()})
		return
	} else if pending {
		c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "an appeal is already pending for this recording"})
		return
	}

	appeal := &models.Appeal{
		RecordingID:  recordingID,
		Appellant:    req.Appellant,
		Reason:       req.Reason,
		DeadlineAt:   time.Now().Add(time.Duration(h.cfg.AppealSLAHours) * time.Hour),
		PrevStatus:   recording.PrevStatus,
		RejectReason: recording.RejectReason,
		RejectedBy:   recording.RejectedBy,
	}

	if err := h.appealRepo.Create(appeal); err != nil {
		if err == repository.ErrAppealPendingConflict {
			c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "an appeal is already pending for this recording"})
			return
		}
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to create appeal", Detail: err.Error()})
		return
	}

	c.JSON(http.StatusCreated, models.APIResponse{Code: 201, Message: "appeal filed", Data: appeal})
}

// List GET /api/v1/appeals — 台账列表，逾期单置顶并带 overdue_count。
func (h *AppealHandler) List(c *gin.Context) {
	var query models.AppealQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid query", Detail: err.Error()})
		return
	}

	appeals, total, overdueCount, err := h.appealRepo.List(query)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to list appeals", Detail: err.Error()})
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: gin.H{
		"items":         appeals,
		"total":         total,
		"overdue_count": overdueCount,
		"offset":        query.Offset,
		"limit":         query.Limit,
	}})
}

// GetByID GET /api/v1/appeals/:id — 申诉单详情。
func (h *AppealHandler) GetByID(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid appeal id"})
		return
	}

	appeal, err := h.appealRepo.GetByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "appeal not found"})
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: appeal})
}

// Review POST /api/v1/appeals/:id/review — 复检：维持或推翻，必须写清依据。
func (h *AppealHandler) Review(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid appeal id"})
		return
	}

	var req models.ReviewAppealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid request", Detail: err.Error()})
		return
	}

	appeal, err := h.appealRepo.Review(id, req.Reviewer, req.Decision, req.Note)
	if err != nil {
		switch err {
		case repository.ErrAppealNotFound:
			c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "appeal not found"})
		case repository.ErrAppealNotPending:
			c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "appeal has already been reviewed"})
		case repository.ErrReviewerIsDecider:
			c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "reviewer cannot be the one who made the rejection decision"})
		case repository.ErrRecordingNotRejected:
			c.JSON(http.StatusConflict, models.ErrorResponse{Code: 409, Message: "recording is no longer in rejected state"})
		default:
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to review appeal", Detail: err.Error()})
		}
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Message: "appeal reviewed", Data: appeal})
}

// Events GET /api/v1/appeals/:id/events — 处理轨迹：谁在什么时候做了什么。
func (h *AppealHandler) Events(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Code: 400, Message: "invalid appeal id"})
		return
	}

	if _, err := h.appealRepo.GetByID(id); err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Code: 404, Message: "appeal not found"})
		return
	}

	events, err := h.appealRepo.ListEvents(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Code: 500, Message: "failed to list appeal events", Detail: err.Error()})
		return
	}

	c.JSON(http.StatusOK, models.APIResponse{Code: 200, Data: gin.H{"items": events}})
}
